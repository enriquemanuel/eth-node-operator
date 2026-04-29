package disk_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/disk"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// fakeRunner records calls and returns preconfigured outputs.
type fakeRunner struct {
	calls   []string
	outputs map[string]string
	errors  map[string]error
}

func newFake() *fakeRunner {
	return &fakeRunner{
		outputs: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (f *fakeRunner) set(out string, name string, args ...string) {
	key := name + " " + strings.Join(args, " ")
	f.outputs[key] = out
}

func (f *fakeRunner) setErr(err error, name string, args ...string) {
	key := name + " " + strings.Join(args, " ")
	f.errors[key] = err
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, key)
	return f.outputs[key], f.errors[key]
}

func (f *fakeRunner) called(name string, args ...string) bool {
	key := name + " " + strings.Join(args, " ")
	for _, c := range f.calls {
		if c == key {
			return true
		}
	}
	return false
}

func (f *fakeRunner) calledContaining(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func lsblkTwoDisks() string {
	return `{
  "blockdevices": [
    {"name":"nvme0n1","path":"/dev/nvme0n1","type":"disk","size":"1.8T","mountpoint":"","fstype":"","children":null},
    {"name":"nvme1n1","path":"/dev/nvme1n1","type":"disk","size":"1.8T","mountpoint":"","fstype":"","children":null}
  ]
}`
}

func lsblkOneDisk() string {
	return `{
  "blockdevices": [
    {"name":"nvme0n1","path":"/dev/nvme0n1","type":"disk","size":"1.8T","mountpoint":"","fstype":"","children":null}
  ]
}`
}

func lsblkNoneEligible() string {
	return `{
  "blockdevices": [
    {"name":"sda","path":"/dev/sda","type":"disk","size":"100G","mountpoint":"/","fstype":"ext4","children":null}
  ]
}`
}

func TestDiscoverDevices_TwoBareDrives(t *testing.T) {
	fake := newFake()
	fake.set(lsblkTwoDisks(), "lsblk", "-J", "-o", "NAME,PATH,TYPE,SIZE,MOUNTPOINT,FSTYPE")

	m := disk.NewWithRunner(fake)
	devices, err := m.DiscoverDevices(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devices) != 2 {
		t.Errorf("expected 2 devices, got %d", len(devices))
	}
}

func TestDiscoverDevices_SkipsMountedAndPartitioned(t *testing.T) {
	fake := newFake()
	fake.set(lsblkNoneEligible(), "lsblk", "-J", "-o", "NAME,PATH,TYPE,SIZE,MOUNTPOINT,FSTYPE")

	m := disk.NewWithRunner(fake)
	devices, err := m.DiscoverDevices(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("expected 0 eligible devices, got %d", len(devices))
	}
}

func TestReconcile_SingleDisk_FormatsAndMounts(t *testing.T) {
	fake := newFake()
	fake.set(lsblkOneDisk(), "lsblk", "-J", "-o", "NAME,PATH,TYPE,SIZE,MOUNTPOINT,FSTYPE")
	fake.set("", "blkid", "-s", "TYPE", "-o", "value", "/dev/nvme0n1") // no fs yet
	fake.set("", "mkfs.ext4", "-F", "-m", "0", "-E", "lazy_itable_init=0,lazy_journal_init=0", "/dev/nvme0n1")
	fake.setErr(fmt.Errorf("not mounted"), "findmnt", "--noheadings", "--target", "/data")
	fake.set("", "mount", "-t", "ext4", "-o", "noatime,nodiratime", "/dev/nvme0n1", "/data")

	m := disk.NewWithRunner(fake)
	spec := types.DiskSpec{
		MountPath: "/data",
		FsType:    "ext4",
		Options:   []string{"noatime", "nodiratime"},
	}

	actions, err := m.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.calledContaining("mkfs.ext4") {
		t.Error("expected mkfs.ext4 to be called")
	}
	if !fake.calledContaining("mount") {
		t.Error("expected mount to be called")
	}

	formatted := false
	mounted := false
	for _, a := range actions {
		if strings.Contains(a, "formatted") {
			formatted = true
		}
		if strings.Contains(a, "mounted") {
			mounted = true
		}
	}
	if !formatted {
		t.Error("expected formatted action")
	}
	if !mounted {
		t.Error("expected mounted action")
	}
}

func TestReconcile_AlreadyFormattedAndMounted_NoOp(t *testing.T) {
	fake := newFake()
	fake.set(lsblkOneDisk(), "lsblk", "-J", "-o", "NAME,PATH,TYPE,SIZE,MOUNTPOINT,FSTYPE")
	fake.set("ext4", "blkid", "-s", "TYPE", "-o", "value", "/dev/nvme0n1") // already formatted
	fake.set("/dev/nvme0n1 /data ext4 noatime 0 0", "findmnt", "--noheadings", "--target", "/data") // already mounted

	m := disk.NewWithRunner(fake)
	spec := types.DiskSpec{
		MountPath: "/data",
		FsType:    "ext4",
		Options:   []string{"noatime"},
	}

	actions, err := m.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fake.calledContaining("mkfs") {
		t.Error("mkfs should NOT be called when already formatted")
	}
	if fake.calledContaining("mount ") {
		t.Error("mount should NOT be called when already mounted")
	}

	// No format or mount actions
	for _, a := range actions {
		if strings.Contains(a, "formatted") || strings.Contains(a, "mounted") {
			t.Errorf("unexpected action on already-provisioned disk: %s", a)
		}
	}
}

func TestReconcile_TwoDrives_AssemblesRAID0(t *testing.T) {
	fake := newFake()
	fake.set(lsblkTwoDisks(), "lsblk", "-J", "-o", "NAME,PATH,TYPE,SIZE,MOUNTPOINT,FSTYPE")

	// RAID0 array does not exist yet
	fake.setErr(fmt.Errorf("no device"), "mdadm", "--detail", "/dev/md0")
	fake.set("", "mdadm", "--create", "/dev/md0", "--level=0", "--raid-devices=2", "--force", "/dev/nvme0n1", "/dev/nvme1n1")
	fake.set("", "bash", "-c", "mdadm --detail --scan >> /etc/mdadm/mdadm.conf")
	fake.set("", "update-initramfs", "-u")

	// After RAID assembly
	fake.set("", "blkid", "-s", "TYPE", "-o", "value", "/dev/md0")
	fake.set("", "mkfs.ext4", "-F", "-m", "0", "-E", "lazy_itable_init=0,lazy_journal_init=0", "/dev/md0")
	fake.setErr(fmt.Errorf("not mounted"), "findmnt", "--noheadings", "--target", "/data")
	fake.set("", "mount", "-t", "ext4", "/dev/md0", "/data")

	m := disk.NewWithRunner(fake)
	spec := types.DiskSpec{
		RaidLevel:   0,
		ArrayDevice: "/dev/md0",
		MountPath:   "/data",
		FsType:      "ext4",
	}

	actions, err := m.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assembled := false
	for _, a := range actions {
		if strings.Contains(a, "RAID0") {
			assembled = true
		}
	}
	if !assembled {
		t.Errorf("expected RAID0 assembly action, got: %v", actions)
	}
}

func TestReconcile_TwoDrives_RAID0AlreadyRunning(t *testing.T) {
	fake := newFake()
	fake.set(lsblkTwoDisks(), "lsblk", "-J", "-o", "NAME,PATH,TYPE,SIZE,MOUNTPOINT,FSTYPE")

	// Array already running
	fake.set("State : clean\n/dev/md0", "mdadm", "--detail", "/dev/md0")
	fake.set("ext4", "blkid", "-s", "TYPE", "-o", "value", "/dev/md0")
	fake.set("/dev/md0 /data ext4", "findmnt", "--noheadings", "--target", "/data")

	m := disk.NewWithRunner(fake)
	spec := types.DiskSpec{
		RaidLevel:   0,
		ArrayDevice: "/dev/md0",
		MountPath:   "/data",
		FsType:      "ext4",
	}

	actions, err := m.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fake.calledContaining("--create") {
		t.Error("mdadm --create should NOT be called when array already running")
	}
	if fake.calledContaining("mkfs") {
		t.Error("mkfs should NOT be called when array already formatted")
	}
	_ = actions
}

func TestReconcile_EmptySpec_NoOp(t *testing.T) {
	fake := newFake()
	m := disk.NewWithRunner(fake)

	actions, err := m.Reconcile(context.Background(), types.DiskSpec{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("expected no actions for empty spec, got %v", actions)
	}
}

func TestIsMounted_WhenMounted(t *testing.T) {
	fake := newFake()
	fake.set("/dev/md0 /data ext4 noatime", "findmnt", "--noheadings", "--target", "/data")

	m := disk.NewWithRunner(fake)
	mounted, err := m.IsMounted(context.Background(), "/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mounted {
		t.Error("expected mounted=true")
	}
}

func TestIsMounted_WhenNotMounted(t *testing.T) {
	fake := newFake()
	fake.setErr(fmt.Errorf("not mounted"), "findmnt", "--noheadings", "--target", "/data")

	m := disk.NewWithRunner(fake)
	mounted, err := m.IsMounted(context.Background(), "/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mounted {
		t.Error("expected mounted=false")
	}
}
