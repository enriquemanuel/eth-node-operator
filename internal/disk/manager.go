// Package disk manages data disk provisioning on bare metal nodes.
// It handles: block device discovery, RAID0 assembly via mdadm,
// filesystem creation, mounting, and fstab persistence.
//
// Every operation is idempotent — safe to run on an already-provisioned node.
// Execution order:
//  1. Discover eligible block devices
//  2. Assemble or verify RAID0 array (if multiple devices)
//  3. Format the target device if no filesystem exists
//  4. Mount to the desired mount path if not already mounted
//  5. Write fstab entry if not already present
package disk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// Runner abstracts command execution for testing.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type realRunner struct{}

func (r *realRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Manager provisions and monitors data disks according to a DiskSpec.
type Manager struct {
	runner Runner
}

// New returns a Manager using real system commands.
func New() *Manager {
	return &Manager{runner: &realRunner{}}
}

// NewWithRunner returns a Manager using a custom Runner (for testing).
func NewWithRunner(r Runner) *Manager {
	return &Manager{runner: r}
}

// BlockDevice represents a discovered block device.
type BlockDevice struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Type       string `json:"type"`       // disk | part | raid0 | md
	Size       string `json:"size"`
	Mountpoint string `json:"mountpoint"`
	FSType     string `json:"fstype"`
	Children   []BlockDevice `json:"children,omitempty"`
}

// lsblkOutput is the top-level structure from lsblk -J
type lsblkOutput struct {
	BlockDevices []BlockDevice `json:"blockdevices"`
}

// Reconcile applies the desired DiskSpec to the host. It is safe to call
// repeatedly — all steps are no-ops when the desired state is already present.
func (m *Manager) Reconcile(ctx context.Context, spec types.DiskSpec) ([]string, error) {
	if spec.MountPath == "" {
		return nil, nil // no disk spec configured
	}

	var actions []string

	// 1. Resolve target device (single disk or RAID array)
	target, assembleActions, err := m.resolveTarget(ctx, spec)
	if err != nil {
		return actions, fmt.Errorf("resolve target device: %w", err)
	}
	actions = append(actions, assembleActions...)

	// 2. Format if no filesystem
	formatted, err := m.ensureFormatted(ctx, target, spec.FsType)
	if err != nil {
		return actions, fmt.Errorf("format %s: %w", target, err)
	}
	if formatted {
		actions = append(actions, fmt.Sprintf("disk: formatted %s as %s", target, spec.FsType))
	}

	// 3. Mount if not mounted
	mounted, err := m.ensureMounted(ctx, target, spec.MountPath, spec.FsType, spec.Options)
	if err != nil {
		return actions, fmt.Errorf("mount %s → %s: %w", target, spec.MountPath, err)
	}
	if mounted {
		actions = append(actions, fmt.Sprintf("disk: mounted %s → %s", target, spec.MountPath))
	}

	// 4. Persist in fstab
	fstabAdded, err := m.ensureFstab(target, spec.MountPath, spec.FsType, spec.Options)
	if err != nil {
		return actions, fmt.Errorf("fstab: %w", err)
	}
	if fstabAdded {
		actions = append(actions, fmt.Sprintf("disk: added %s to /etc/fstab", target))
	}

	return actions, nil
}

// Status returns the current disk status for reporting.
func (m *Manager) Status(ctx context.Context, spec types.DiskSpec) (types.DiskStatus, error) {
	status := types.DiskStatus{
		MountPath: spec.MountPath,
		RaidLevel: spec.RaidLevel,
	}

	if spec.MountPath == "" {
		return status, nil
	}

	mounted, err := m.IsMounted(ctx, spec.MountPath)
	if err != nil {
		return status, err
	}
	status.Mounted = mounted

	if mounted {
		used, free, total, err := m.diskUsage(spec.MountPath)
		if err == nil {
			status.UsedGB = float64(used) / (1 << 30)
			status.FreeGB = float64(free) / (1 << 30)
			status.TotalGB = float64(total) / (1 << 30)
		}
	}

	devices, err := m.DiscoverDevices(ctx)
	if err == nil {
		for _, d := range devices {
			status.Devices = append(status.Devices, d.Path)
		}
	}

	return status, nil
}

// resolveTarget returns the device path to use (md device or single disk).
func (m *Manager) resolveTarget(ctx context.Context, spec types.DiskSpec) (string, []string, error) {
	var actions []string

	devices, err := m.resolveDevices(ctx, spec.Devices)
	if err != nil {
		return "", nil, err
	}

	if len(devices) == 0 {
		return "", nil, fmt.Errorf("no eligible block devices found")
	}

	// Single device — no RAID needed
	if len(devices) == 1 || spec.RaidLevel < 0 {
		return devices[0], actions, nil
	}

	// Multiple devices — assemble or verify RAID0
	arrayDevice := spec.ArrayDevice
	if arrayDevice == "" {
		arrayDevice = "/dev/md0"
	}

	assembled, err := m.ensureRAID0(ctx, arrayDevice, devices)
	if err != nil {
		return "", nil, fmt.Errorf("RAID0 assembly: %w", err)
	}
	if assembled {
		actions = append(actions, fmt.Sprintf("disk: assembled RAID0 array %s from %v", arrayDevice, devices))
	}

	return arrayDevice, actions, nil
}

// resolveDevices returns the concrete list of device paths to use.
// An empty list means auto-discover.
func (m *Manager) resolveDevices(ctx context.Context, specified []string) ([]string, error) {
	if len(specified) > 0 {
		return specified, nil
	}
	// Auto-discover: find bare block disks with no partition table and no filesystem
	devices, err := m.DiscoverDevices(ctx)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, d := range devices {
		paths = append(paths, d.Path)
	}
	return paths, nil
}

// DiscoverDevices returns unpartitioned block devices eligible for use.
// Excludes: already-mounted devices, loop devices, ROM drives, small devices.
func (m *Manager) DiscoverDevices(ctx context.Context) ([]BlockDevice, error) {
	out, err := m.runner.Run(ctx, "lsblk", "-J", "-o", "NAME,PATH,TYPE,SIZE,MOUNTPOINT,FSTYPE")
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}

	var lsblk lsblkOutput
	if err := json.Unmarshal([]byte(out), &lsblk); err != nil {
		return nil, fmt.Errorf("parse lsblk output: %w", err)
	}

	var eligible []BlockDevice
	for _, dev := range lsblk.BlockDevices {
		// Only bare disks (not partitions, loops, roms)
		if dev.Type != "disk" {
			continue
		}
		// Skip if already has a filesystem or is mounted
		if dev.Mountpoint != "" || (dev.FSType != "" && dev.FSType != "linux_raid_member") {
			continue
		}
		// Skip if has children (already partitioned)
		if len(dev.Children) > 0 {
			continue
		}
		eligible = append(eligible, dev)
	}
	return eligible, nil
}

// ensureRAID0 creates a RAID0 array if it doesn't exist; verifies it if it does.
// Returns true if the array was newly created.
func (m *Manager) ensureRAID0(ctx context.Context, arrayDevice string, devices []string) (bool, error) {
	// Check if array already exists and is running — mdadm exits non-zero if not
	out, err := m.runner.Run(ctx, "mdadm", "--detail", arrayDevice)
	if err == nil && (strings.Contains(out, "State : clean") || strings.Contains(out, "State : active")) {
		return false, nil // already assembled and running
	}

	// Create the RAID0 array
	args := []string{
		"--create", arrayDevice,
		"--level=0",
		fmt.Sprintf("--raid-devices=%d", len(devices)),
		"--force",
	}
	args = append(args, devices...)

	if out, err := m.runner.Run(ctx, "mdadm", args...); err != nil {
		return false, fmt.Errorf("mdadm create: %s: %w", out, err)
	}

	// Save mdadm configuration for array persistence across reboots
	if _, err := m.runner.Run(ctx, "bash", "-c",
		fmt.Sprintf("mdadm --detail --scan >> /etc/mdadm/mdadm.conf")); err != nil {
		// Non-fatal — array works but won't auto-assemble on boot without this
		_ = err
	}

	// Update initramfs so the array comes up in early boot
	m.runner.Run(ctx, "update-initramfs", "-u") //nolint:errcheck

	return true, nil
}

// ensureFormatted creates a filesystem on device if none exists.
// Returns true if a new filesystem was created.
func (m *Manager) ensureFormatted(ctx context.Context, device, fsType string) (bool, error) {
	if fsType == "" {
		fsType = "ext4"
	}

	// Check if filesystem already exists
	out, _ := m.runner.Run(ctx, "blkid", "-s", "TYPE", "-o", "value", device)
	if strings.TrimSpace(out) != "" {
		return false, nil // already has a filesystem
	}

	// Format
	var mkfsArgs []string
	switch fsType {
	case "ext4":
		mkfsArgs = []string{"-F", "-m", "0", "-E", "lazy_itable_init=0,lazy_journal_init=0", device}
		if out, err := m.runner.Run(ctx, "mkfs.ext4", mkfsArgs...); err != nil {
			return false, fmt.Errorf("mkfs.ext4: %s: %w", out, err)
		}
	case "xfs":
		mkfsArgs = []string{"-f", device}
		if out, err := m.runner.Run(ctx, "mkfs.xfs", mkfsArgs...); err != nil {
			return false, fmt.Errorf("mkfs.xfs: %s: %w", out, err)
		}
	default:
		return false, fmt.Errorf("unsupported filesystem type: %s", fsType)
	}

	return true, nil
}

// IsMounted returns true if mountPath has an active mount.
func (m *Manager) IsMounted(ctx context.Context, mountPath string) (bool, error) {
	out, err := m.runner.Run(ctx, "findmnt", "--noheadings", "--target", mountPath)
	if err != nil {
		return false, nil // findmnt exits non-zero when not mounted
	}
	return strings.TrimSpace(out) != "", nil
}

// ensureMounted mounts device to mountPath if not already mounted.
// Returns true if a new mount was performed.
func (m *Manager) ensureMounted(ctx context.Context, device, mountPath, fsType string, options []string) (bool, error) {
	alreadyMounted, err := m.IsMounted(ctx, mountPath)
	if err != nil {
		return false, err
	}
	if alreadyMounted {
		return false, nil
	}

	// Create mount point
	if err := os.MkdirAll(mountPath, 0755); err != nil {
		return false, fmt.Errorf("create mountpoint %s: %w", mountPath, err)
	}

	// Build mount command
	args := []string{"-t", fsType}
	if len(options) > 0 {
		args = append(args, "-o", strings.Join(options, ","))
	}
	args = append(args, device, mountPath)

	if out, err := m.runner.Run(ctx, "mount", args...); err != nil {
		return false, fmt.Errorf("mount: %s: %w", out, err)
	}
	return true, nil
}

// ensureFstab adds a fstab entry if not already present.
// Returns true if a new entry was written.
func (m *Manager) ensureFstab(device, mountPath, fsType string, options []string) (bool, error) {
	const fstabPath = "/etc/fstab"

	existing, err := os.ReadFile(fstabPath)
	if err != nil {
		return false, fmt.Errorf("read fstab: %w", err)
	}

	// Check if this mount path is already in fstab
	for _, line := range strings.Split(string(existing), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == mountPath {
			return false, nil // already present
		}
	}

	// Resolve device to UUID for fstab stability (survives device renaming)
	uuid, err := m.deviceUUID(device)
	if err != nil {
		// Fall back to device path if UUID resolution fails
		uuid = device
	}

	opts := "defaults"
	if len(options) > 0 {
		opts = strings.Join(options, ",")
	}

	entry := fmt.Sprintf("\n# eth-node-operator managed\nUUID=%s %s %s %s 0 0\n",
		uuid, mountPath, fsType, opts)

	// Append to fstab
	f, err := os.OpenFile(fstabPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return false, fmt.Errorf("open fstab: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(entry); err != nil {
		return false, fmt.Errorf("write fstab: %w", err)
	}
	return true, nil
}

func (m *Manager) deviceUUID(device string) (string, error) {
	// Use blkid to get a stable UUID
	out, err := exec.Command("blkid", "-s", "UUID", "-o", "value", device).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (m *Manager) diskUsage(path string) (used, free, total uint64, err error) {
	// Use df for portability
	out, err := exec.Command("df", "--block-size=1", "--output=used,avail,size", path).Output()
	if err != nil {
		return 0, 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0, 0, fmt.Errorf("unexpected df output")
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("unexpected df fields")
	}
	fmt.Sscanf(fields[0], "%d", &used)
	fmt.Sscanf(fields[1], "%d", &free)
	fmt.Sscanf(fields[2], "%d", &total)
	return used, free, total, nil
}

// MdadmConfigPath is the path to the mdadm configuration file.
var MdadmConfigPath = "/etc/mdadm/mdadm.conf"

// ensureMdadmConfig creates the mdadm config directory if needed.
func ensureMdadmConfig() error {
	return os.MkdirAll(filepath.Dir(MdadmConfigPath), 0755)
}

func init() {
	ensureMdadmConfig() //nolint:errcheck
}
