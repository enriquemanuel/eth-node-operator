package inventory

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
	"gopkg.in/yaml.v3"
)

// LoadNode reads a single EthereumNode YAML file.
func LoadNode(path string) (*types.EthereumNode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read node file %s: %w", path, err)
	}
	var node types.EthereumNode
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("parse node file %s: %w", path, err)
	}
	return &node, nil
}

// LoadAll reads all node YAML files from a directory.
func LoadAll(dir string) ([]*types.EthereumNode, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read inventory dir %s: %w", dir, err)
	}

	var nodes []*types.EthereumNode
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		node, err := LoadNode(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// LoadAllWithProfiles reads all nodes and merges their declared profiles.
func LoadAllWithProfiles(nodesDir, profilesDir string) ([]*types.EthereumNode, error) {
	nodes, err := LoadAll(nodesDir)
	if err != nil {
		return nil, err
	}

	// Load profiles if directory exists
	profiles := make(map[string]types.Profile)
	if _, err := os.Stat(profilesDir); err == nil {
		entries, err := os.ReadDir(profilesDir)
		if err != nil {
			return nil, fmt.Errorf("read profiles dir: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(profilesDir, e.Name()))
			if err != nil {
				return nil, err
			}
			var p types.Profile
			if err := yaml.Unmarshal(data, &p); err != nil {
				return nil, err
			}
			profiles[p.Name] = p
		}
	}

	// For each node, apply profile merge
	for _, node := range nodes {
		if len(node.Profiles) == 0 {
			continue
		}
		merged, err := mergeNodeProfiles(profiles, node)
		if err != nil {
			return nil, fmt.Errorf("merge profiles for %s: %w", node.Name, err)
		}
		node.Spec = merged
	}

	return nodes, nil
}

func mergeNodeProfiles(profiles map[string]types.Profile, node *types.EthereumNode) (types.NodeSpec, error) {
	base := types.NodeSpec{}
	for _, name := range node.Profiles {
		p, ok := profiles[name]
		if !ok {
			return base, fmt.Errorf("profile %q not found", name)
		}
		base = mergeSpecs(base, p.Spec)
	}
	return mergeSpecs(base, node.Spec), nil
}

// mergeSpecs merges src on top of dst.
func mergeSpecs(dst, src types.NodeSpec) types.NodeSpec {
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

	if src.Consensus.Client != "" {
		dst.Consensus.Client = src.Consensus.Client
	}
	if src.Consensus.Image != "" {
		dst.Consensus.Image = src.Consensus.Image
	}
	if src.Consensus.DataDir != "" {
		dst.Consensus.DataDir = src.Consensus.DataDir
	}

	if src.MEV.Enabled {
		dst.MEV = src.MEV
	}

	if src.Network.Firewall.Provider != "" {
		dst.Network.Firewall.Provider = src.Network.Firewall.Provider
	}
	if src.Network.Firewall.Policy != "" {
		dst.Network.Firewall.Policy = src.Network.Firewall.Policy
	}
	if len(src.Network.Firewall.Rules) > 0 {
		dst.Network.Firewall.Rules = append(dst.Network.Firewall.Rules, src.Network.Firewall.Rules...)
	}

	if len(src.System.Packages) > 0 {
		dst.System.Packages = append(dst.System.Packages, src.System.Packages...)
	}

	if src.Maintenance.Window.Schedule != "" {
		dst.Maintenance.Window.Schedule = src.Maintenance.Window.Schedule
	}

	return dst
}
