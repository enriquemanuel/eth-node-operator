package inventory_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/pkg/inventory"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

func writeCluster(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write cluster: %v", err)
	}
	return path
}

func writeProfileFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

func TestLoadCluster_BasicCluster(t *testing.T) {
	dir := t.TempDir()
	path := writeCluster(t, dir, "mainnet", `
name: mainnet-validators
profiles:
  - mainnet-base
defaultPort: 9000
nodes:
  - name: bare-metal-01
    host: 10.1.1.1
  - name: bare-metal-02
    host: 10.1.1.2
`)

	c, err := inventory.LoadCluster(path)
	if err != nil {
		t.Fatalf("load cluster: %v", err)
	}
	if c.Name != "mainnet-validators" {
		t.Errorf("expected name mainnet-validators, got %s", c.Name)
	}
	if len(c.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(c.Nodes))
	}
}

func TestLoadCluster_MissingFile(t *testing.T) {
	_, err := inventory.LoadCluster("/does/not/exist.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestExpandCluster_AppliesClusterProfiles(t *testing.T) {
	profiles := map[string]types.Profile{
		"mainnet-base": {
			Name: "mainnet-base",
			Spec: types.NodeSpec{
				Execution: types.ClientSpec{Client: "geth", Image: "ethereum/client-go:v1.14.8"},
				Consensus: types.ClientSpec{Client: "lighthouse", Image: "sigp/lighthouse:v5.3.0"},
			},
		},
	}

	cluster := &types.Cluster{
		Name:        "mainnet",
		Profiles:    []string{"mainnet-base"},
		DefaultPort: 9000,
		Nodes: []types.ClusterNode{
			{Name: "bare-metal-01", Host: "10.1.1.1"},
			{Name: "bare-metal-02", Host: "10.1.1.2"},
		},
	}

	nodes, err := inventory.ExpandCluster(cluster, profiles)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	for _, n := range nodes {
		if n.Spec.Execution.Client != "geth" {
			t.Errorf("node %s: expected geth, got %s", n.Name, n.Spec.Execution.Client)
		}
		if n.Port != 9000 {
			t.Errorf("node %s: expected port 9000, got %d", n.Name, n.Port)
		}
	}
}

func TestExpandCluster_NodeOverridesClusterProfile(t *testing.T) {
	profiles := map[string]types.Profile{
		"mainnet-base": {
			Name: "mainnet-base",
			Spec: types.NodeSpec{
				Execution: types.ClientSpec{Image: "ethereum/client-go:v1.14.8"},
			},
		},
	}

	cluster := &types.Cluster{
		Profiles:    []string{"mainnet-base"},
		DefaultPort: 9000,
		Nodes: []types.ClusterNode{
			{
				Name: "bare-metal-03",
				Host: "10.1.1.3",
				Spec: types.NodeSpec{
					Execution: types.ClientSpec{Image: "ethereum/client-go:v1.14.9"},
				},
			},
		},
	}

	nodes, err := inventory.ExpandCluster(cluster, profiles)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	if nodes[0].Spec.Execution.Image != "ethereum/client-go:v1.14.9" {
		t.Errorf("node override should win, got %s", nodes[0].Spec.Execution.Image)
	}
}

func TestExpandCluster_NodeExtraProfiles(t *testing.T) {
	profiles := map[string]types.Profile{
		"mainnet-base": {
			Name: "mainnet-base",
			Spec: types.NodeSpec{
				Execution: types.ClientSpec{Client: "geth"},
			},
		},
		"mev-standard": {
			Name: "mev-standard",
			Spec: types.NodeSpec{
				MEV: types.MEVSpec{Enabled: true, Image: "flashbots/mev-boost:v1.8.1"},
			},
		},
	}

	cluster := &types.Cluster{
		Profiles:    []string{"mainnet-base"},
		DefaultPort: 9000,
		Nodes: []types.ClusterNode{
			{
				Name:     "bare-metal-01",
				Host:     "10.1.1.1",
				Profiles: []string{"mev-standard"}, // this node gets MEV, others don't
			},
			{
				Name: "bare-metal-02",
				Host: "10.1.1.2",
			},
		},
	}

	nodes, err := inventory.ExpandCluster(cluster, profiles)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	if !nodes[0].Spec.MEV.Enabled {
		t.Error("bare-metal-01 should have MEV enabled")
	}
	if nodes[1].Spec.MEV.Enabled {
		t.Error("bare-metal-02 should not have MEV enabled")
	}
}

func TestExpandCluster_SkipsDisabledNodes(t *testing.T) {
	cluster := &types.Cluster{
		DefaultPort: 9000,
		Nodes: []types.ClusterNode{
			{Name: "active-01", Host: "10.1.1.1"},
			{Name: "disabled-02", Host: "10.1.1.2", Disabled: true},
			{Name: "active-03", Host: "10.1.1.3"},
		},
	}

	nodes, err := inventory.ExpandCluster(cluster, map[string]types.Profile{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	if len(nodes) != 2 {
		t.Errorf("expected 2 active nodes, got %d", len(nodes))
	}
	for _, n := range nodes {
		if n.Name == "disabled-02" {
			t.Error("disabled node should be excluded")
		}
	}
}

func TestExpandCluster_NodePortOverridesDefault(t *testing.T) {
	cluster := &types.Cluster{
		DefaultPort: 9000,
		Nodes: []types.ClusterNode{
			{Name: "n1", Host: "10.1.1.1"},
			{Name: "n2", Host: "10.1.1.2", Port: 9001},
		},
	}

	nodes, err := inventory.ExpandCluster(cluster, map[string]types.Profile{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if nodes[0].Port != 9000 {
		t.Errorf("n1 should use default port 9000, got %d", nodes[0].Port)
	}
	if nodes[1].Port != 9001 {
		t.Errorf("n2 should use override port 9001, got %d", nodes[1].Port)
	}
}

func TestExpandCluster_UnknownProfileReturnsError(t *testing.T) {
	cluster := &types.Cluster{
		Profiles: []string{"does-not-exist"},
		Nodes:    []types.ClusterNode{{Name: "n1", Host: "10.1.1.1"}},
	}

	_, err := inventory.ExpandCluster(cluster, map[string]types.Profile{})
	if err == nil {
		t.Error("expected error for unknown profile")
	}
}

func TestLoadClusterNodes_IntegrationWithProfilesDir(t *testing.T) {
	dir := t.TempDir()
	profilesDir := t.TempDir()

	writeProfileFile(t, profilesDir, "mainnet-base", `
name: mainnet-base
spec:
  execution:
    client: geth
    image: ethereum/client-go:v1.14.8
`)

	clusterPath := writeCluster(t, dir, "validators", `
name: mainnet-validators
profiles:
  - mainnet-base
defaultPort: 9000
nodes:
  - name: bare-metal-01
    host: 10.1.1.1
  - name: bare-metal-02
    host: 10.1.1.2
    spec:
      execution:
        image: ethereum/client-go:v1.14.9
`)

	nodes, err := inventory.LoadClusterNodes(clusterPath, profilesDir)
	if err != nil {
		t.Fatalf("load cluster nodes: %v", err)
	}

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	if nodes[0].Spec.Execution.Image != "ethereum/client-go:v1.14.8" {
		t.Errorf("node 1 should use profile image, got %s", nodes[0].Spec.Execution.Image)
	}
	if nodes[1].Spec.Execution.Image != "ethereum/client-go:v1.14.9" {
		t.Errorf("node 2 should use override image, got %s", nodes[1].Spec.Execution.Image)
	}
}

func TestLoadAllClusters_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeCluster(t, dir, "mainnet", `name: mainnet
nodes: []`)
	writeCluster(t, dir, "testnet", `name: testnet
nodes: []`)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# docs"), 0644)

	clusters, err := inventory.LoadAllClusters(dir)
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	if len(clusters) != 2 {
		t.Errorf("expected 2 clusters, got %d", len(clusters))
	}
}
