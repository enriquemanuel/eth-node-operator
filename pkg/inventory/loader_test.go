package inventory_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/pkg/inventory"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoadNode_BasicNode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bare-metal-01.yaml", `
name: bare-metal-01
host: 10.1.1.1
spec:
  execution:
    client: geth
    image: ethereum/client-go:v1.14.8
  consensus:
    client: lighthouse
    image: sigp/lighthouse:v5.3.0
`)

	node, err := inventory.LoadNode(filepath.Join(dir, "bare-metal-01.yaml"))
	if err != nil {
		t.Fatalf("load node: %v", err)
	}

	if node.Name != "bare-metal-01" {
		t.Errorf("expected name bare-metal-01, got %s", node.Name)
	}
	if node.Host != "10.1.1.1" {
		t.Errorf("expected host 10.1.1.1, got %s", node.Host)
	}
	if node.Spec.Execution.Client != "geth" {
		t.Errorf("expected geth, got %s", node.Spec.Execution.Client)
	}
}

func TestLoadNode_MissingFile(t *testing.T) {
	_, err := inventory.LoadNode("/does/not/exist.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadNode_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.yaml", "this: is: invalid: yaml: :")

	_, err := inventory.LoadNode(filepath.Join(dir, "bad.yaml"))
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadAll_MultipleNodes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node-01.yaml", `name: node-01
host: 10.1.1.1`)
	writeFile(t, dir, "node-02.yaml", `name: node-02
host: 10.1.1.2`)
	writeFile(t, dir, "README.md", "# docs") // should be ignored

	nodes, err := inventory.LoadAll(dir)
	if err != nil {
		t.Fatalf("load all: %v", err)
	}

	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(nodes))
	}
}

func TestLoadAll_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	nodes, err := inventory.LoadAll(dir)
	if err != nil {
		t.Fatalf("load all empty: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestLoadAllWithProfiles_MergesCorrectly(t *testing.T) {
	nodesDir := t.TempDir()
	profilesDir := t.TempDir()

	writeFile(t, profilesDir, "mainnet-base.yaml", `
name: mainnet-base
spec:
  execution:
    client: geth
    image: ethereum/client-go:v1.14.8
  consensus:
    client: lighthouse
    image: sigp/lighthouse:v5.3.0
`)

	writeFile(t, nodesDir, "bare-metal-01.yaml", `
name: bare-metal-01
host: 10.1.1.1
profiles:
  - mainnet-base
spec:
  consensus:
    image: sigp/lighthouse:v5.4.0
`)

	nodes, err := inventory.LoadAllWithProfiles(nodesDir, profilesDir)
	if err != nil {
		t.Fatalf("load with profiles: %v", err)
	}

	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}

	node := nodes[0]

	// Profile provides the EL client
	if node.Spec.Execution.Client != "geth" {
		t.Errorf("expected geth from profile, got %s", node.Spec.Execution.Client)
	}

	// Node overrides the CL image
	if node.Spec.Consensus.Image != "sigp/lighthouse:v5.4.0" {
		t.Errorf("expected lh v5.4.0 from node override, got %s", node.Spec.Consensus.Image)
	}
}

func TestLoadAllWithProfiles_MissingProfile(t *testing.T) {
	nodesDir := t.TempDir()
	profilesDir := t.TempDir()

	writeFile(t, nodesDir, "node.yaml", `
name: node-01
host: 10.1.1.1
profiles:
  - does-not-exist
`)

	_, err := inventory.LoadAllWithProfiles(nodesDir, profilesDir)
	if err == nil {
		t.Error("expected error for missing profile")
	}
}

func TestLoadAllWithProfiles_NoProfilesDir(t *testing.T) {
	nodesDir := t.TempDir()
	writeFile(t, nodesDir, "node.yaml", `name: node-01
host: 10.1.1.1`)

	// Non-existent profiles dir is ok if no node uses profiles
	nodes, err := inventory.LoadAllWithProfiles(nodesDir, "/does/not/exist")
	if err != nil {
		t.Fatalf("expected no error: %v", err)
	}
	if len(nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(nodes))
	}
}
