package profile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
	"gopkg.in/yaml.v3"
)

// Loader loads profiles from a directory tree (recursive).
type Loader struct {
	dir string
}

// NewLoader returns a Loader that reads profiles from dir and all subdirectories.
func NewLoader(dir string) *Loader {
	return &Loader{dir: dir}
}

// Load reads all profiles from the directory tree and returns them keyed by name.
// Subdirectories are walked recursively — put client pairs in profiles/clients/,
// feature profiles in profiles/, etc.
func (l *Loader) Load() (map[string]types.Profile, error) {
	profiles := make(map[string]types.Profile)

	err := filepath.WalkDir(l.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil // recurse into subdirs
		}
		if filepath.Ext(d.Name()) != ".yaml" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read profile %s: %w", path, err)
		}
		var p types.Profile
		if err := yaml.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("parse profile %s: %w", path, err)
		}
		if p.Name == "" {
			return fmt.Errorf("profile %s has no name field", path)
		}
		if _, exists := profiles[p.Name]; exists {
			return fmt.Errorf("duplicate profile name %q (found in %s)", p.Name, path)
		}
		profiles[p.Name] = p
		return nil
	})

	return profiles, err
}

// Merge applies a list of named profiles to a base NodeSpec, then applies
// the node-level overrides on top. Later profiles win over earlier ones.
// Node overrides always win over profiles.
func Merge(profiles map[string]types.Profile, profileNames []string, nodeSpec types.NodeSpec) (types.NodeSpec, error) {
	base := types.NodeSpec{}

	for _, name := range profileNames {
		p, ok := profiles[name]
		if !ok {
			return base, fmt.Errorf("profile %q not found", name)
		}
		base = mergeSpecs(base, p.Spec)
	}

	return mergeSpecs(base, nodeSpec), nil
}

// mergeSpecs merges src on top of dst. Non-zero fields in src overwrite dst.
func mergeSpecs(dst, src types.NodeSpec) types.NodeSpec {
	// System
	if len(src.System.Packages) > 0 {
		dst.System.Packages = mergePackages(dst.System.Packages, src.System.Packages)
	}
	if len(src.System.Kernel.Parameters) > 0 {
		if dst.System.Kernel.Parameters == nil {
			dst.System.Kernel.Parameters = make(map[string]string)
		}
		for k, v := range src.System.Kernel.Parameters {
			dst.System.Kernel.Parameters[k] = v
		}
	}
	if src.System.Disk.MountPath != "" {
		dst.System.Disk = src.System.Disk
	}

	// Network
	if len(src.Network.DNS.Nameservers) > 0 {
		dst.Network.DNS.Nameservers = src.Network.DNS.Nameservers
	}
	if src.Network.Firewall.Provider != "" {
		dst.Network.Firewall.Provider = src.Network.Firewall.Provider
	}
	if src.Network.Firewall.Policy != "" {
		dst.Network.Firewall.Policy = src.Network.Firewall.Policy
	}
	if len(src.Network.Firewall.Rules) > 0 {
		dst.Network.Firewall.Rules = mergeFirewallRules(dst.Network.Firewall.Rules, src.Network.Firewall.Rules)
	}

	// Execution client
	if src.Execution.Client != "" {
		dst.Execution.Client = src.Execution.Client
	}
	if src.Execution.Image != "" {
		dst.Execution.Image = src.Execution.Image
	}
	if src.Execution.DataDir != "" {
		dst.Execution.DataDir = src.Execution.DataDir
	}
	if len(src.Execution.Flags) > 0 {
		if dst.Execution.Flags == nil {
			dst.Execution.Flags = make(map[string]string)
		}
		for k, v := range src.Execution.Flags {
			dst.Execution.Flags[k] = v
		}
	}
	if src.Execution.Ports.HTTP != 0 {
		dst.Execution.Ports = src.Execution.Ports
	}

	// Consensus client
	if src.Consensus.Client != "" {
		dst.Consensus.Client = src.Consensus.Client
	}
	if src.Consensus.Image != "" {
		dst.Consensus.Image = src.Consensus.Image
	}
	if src.Consensus.DataDir != "" {
		dst.Consensus.DataDir = src.Consensus.DataDir
	}
	if len(src.Consensus.Flags) > 0 {
		if dst.Consensus.Flags == nil {
			dst.Consensus.Flags = make(map[string]string)
		}
		for k, v := range src.Consensus.Flags {
			dst.Consensus.Flags[k] = v
		}
	}
	if src.Consensus.Ports.HTTP != 0 {
		dst.Consensus.Ports = src.Consensus.Ports
	}

	// MEV
	if src.MEV.Enabled {
		dst.MEV.Enabled = true
	}
	if src.MEV.Image != "" {
		dst.MEV.Image = src.MEV.Image
	}
	if src.MEV.ListenAddr != "" {
		dst.MEV.ListenAddr = src.MEV.ListenAddr
	}
	if len(src.MEV.Relays) > 0 {
		dst.MEV.Relays = src.MEV.Relays
	}

	// Observability
	if src.Observability.Metrics.Enabled {
		dst.Observability.Metrics.Enabled = true
	}
	if len(src.Observability.Metrics.Exporters) > 0 {
		dst.Observability.Metrics.Exporters = src.Observability.Metrics.Exporters
	}
	if len(src.Observability.Metrics.RemoteWrite) > 0 {
		dst.Observability.Metrics.RemoteWrite = src.Observability.Metrics.RemoteWrite
	}
	if src.Observability.Logs.Provider != "" {
		dst.Observability.Logs.Provider = src.Observability.Logs.Provider
	}
	if len(src.Observability.Logs.Destinations) > 0 {
		dst.Observability.Logs.Destinations = src.Observability.Logs.Destinations
	}
	if src.Observability.StackDir != "" {
		dst.Observability.StackDir = src.Observability.StackDir
	}

	// Route 53 DNS
	if src.Network.Route53.ZoneID != "" {
		dst.Network.Route53.ZoneID = src.Network.Route53.ZoneID
	}
	if src.Network.Route53.Zone != "" {
		dst.Network.Route53.Zone = src.Network.Route53.Zone
	}
	if src.Network.Route53.TTL != 0 {
		dst.Network.Route53.TTL = src.Network.Route53.TTL
	}

	// VCGateways (additive — collect from all profiles + node)
	if len(src.Network.VCGateways) > 0 {
		dst.Network.VCGateways = append(dst.Network.VCGateways, src.Network.VCGateways...)
	}

	// Snapshot
	if src.Snapshot.Enabled {
		dst.Snapshot.Enabled = true
	}
	if src.Snapshot.Provider != "" {
		dst.Snapshot.Provider = src.Snapshot.Provider
	}
	if src.Snapshot.Network != "" {
		dst.Snapshot.Network = src.Snapshot.Network
	}
	if src.Snapshot.ELEnabled {
		dst.Snapshot.ELEnabled = true
	}
	if src.Snapshot.CLEnabled {
		dst.Snapshot.CLEnabled = true
	}
	if src.Snapshot.BlockNumber != "" {
		dst.Snapshot.BlockNumber = src.Snapshot.BlockNumber
	}


	// Maintenance
	if src.Maintenance.Window.Schedule != "" {
		dst.Maintenance.Window.Schedule = src.Maintenance.Window.Schedule
	}
	if len(src.Maintenance.UpgradeStrategy.Order) > 0 {
		dst.Maintenance.UpgradeStrategy.Order = src.Maintenance.UpgradeStrategy.Order
	}
	if len(src.Maintenance.UpgradeStrategy.PreflightChecks) > 0 {
		dst.Maintenance.UpgradeStrategy.PreflightChecks = src.Maintenance.UpgradeStrategy.PreflightChecks
	}

	return dst
}

func mergePackages(dst, src []types.PackageSpec) []types.PackageSpec {
	index := make(map[string]int, len(dst))
	for i, p := range dst {
		index[p.Name] = i
	}
	for _, p := range src {
		if i, ok := index[p.Name]; ok {
			dst[i] = p
		} else {
			dst = append(dst, p)
		}
	}
	return dst
}

func mergeFirewallRules(dst, src []types.FirewallRule) []types.FirewallRule {
	index := make(map[string]int, len(dst))
	for i, r := range dst {
		key := fmt.Sprintf("%d/%s/%s", r.Port, r.Proto, r.Direction)
		index[key] = i
	}
	for _, r := range src {
		key := fmt.Sprintf("%d/%s/%s", r.Port, r.Proto, r.Direction)
		if i, ok := index[key]; ok {
			dst[i] = r
		} else {
			dst = append(dst, r)
		}
	}
	return dst
}
