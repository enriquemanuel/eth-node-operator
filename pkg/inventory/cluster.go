package inventory

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
	"gopkg.in/yaml.v3"
)

// LoadCluster reads a cluster YAML file and returns a Cluster.
func LoadCluster(path string) (*types.Cluster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cluster %s: %w", path, err)
	}
	var c types.Cluster
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse cluster %s: %w", path, err)
	}
	return &c, nil
}

// LoadAllClusters reads all cluster YAML files from a directory.
func LoadAllClusters(dir string) ([]*types.Cluster, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read clusters dir %s: %w", dir, err)
	}
	var clusters []*types.Cluster
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		c, err := LoadCluster(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, c)
	}
	return clusters, nil
}

// ExpandCluster resolves a Cluster into a flat list of EthereumNodes
// by merging cluster-level profiles → node-level profiles → node spec overrides.
// Disabled nodes are excluded.
func ExpandCluster(cluster *types.Cluster, profiles map[string]types.Profile) ([]*types.EthereumNode, error) {
	port := cluster.DefaultPort
	if port == 0 {
		port = 9000
	}

	var nodes []*types.EthereumNode
	for _, cn := range cluster.Nodes {
		if cn.Disabled {
			continue
		}

		// Merge order: cluster profiles → node extra profiles → node spec
		allProfiles := append(cluster.Profiles, cn.Profiles...)

		base := types.NodeSpec{}
		for _, name := range allProfiles {
			p, ok := profiles[name]
			if !ok {
				return nil, fmt.Errorf("node %s: profile %q not found", cn.Name, name)
			}
			base = mergeSpecs(base, p.Spec)
		}
		spec := mergeSpecs(base, cn.Spec)

		nodePort := cn.Port
		if nodePort == 0 {
			nodePort = port
		}

		nodes = append(nodes, &types.EthereumNode{
			Name:     cn.Name,
			Host:     cn.Host,
			Port:     nodePort,
			Labels:   cn.Labels,
			Profiles: allProfiles,
			Spec:     spec,
		})
	}
	return nodes, nil
}

// LoadClusterNodes is a convenience that loads a cluster + its profiles
// and returns the expanded node list ready for use by ethctl and the reconciler.
func LoadClusterNodes(clusterPath, profilesDir string) ([]*types.EthereumNode, error) {
	cluster, err := LoadCluster(clusterPath)
	if err != nil {
		return nil, err
	}

	profiles := make(map[string]types.Profile)
	if _, statErr := os.Stat(profilesDir); statErr == nil {
		entries, err := os.ReadDir(profilesDir)
		if err != nil {
			return nil, fmt.Errorf("read profiles: %w", err)
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

	return ExpandCluster(cluster, profiles)
}
