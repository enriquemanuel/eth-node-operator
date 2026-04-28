package profile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/pkg/profile"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

func writeProfile(t *testing.T, dir, name, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

func TestMerge_ProfileSetsClientWhenNodeDoesNot(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "mainnet-base", `
name: mainnet-base
spec:
  execution:
    client: geth
    image: ethereum/client-go:v1.14.8
  consensus:
    client: lighthouse
    image: sigp/lighthouse:v5.3.0
`)

	loader := profile.NewLoader(dir)
	profiles, err := loader.Load()
	if err != nil {
		t.Fatalf("load profiles: %v", err)
	}

	resolved, err := profile.Merge(profiles, []string{"mainnet-base"}, types.NodeSpec{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if resolved.Execution.Client != "geth" {
		t.Errorf("expected geth, got %s", resolved.Execution.Client)
	}
	if resolved.Consensus.Client != "lighthouse" {
		t.Errorf("expected lighthouse, got %s", resolved.Consensus.Client)
	}
}

func TestMerge_NodeOverridesProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "mainnet-base", `
name: mainnet-base
spec:
  consensus:
    client: lighthouse
    image: sigp/lighthouse:v5.3.0
`)

	loader := profile.NewLoader(dir)
	profiles, _ := loader.Load()

	nodeSpec := types.NodeSpec{
		Consensus: types.ClientSpec{
			Image: "sigp/lighthouse:v5.4.0", // override the profile image
		},
	}

	resolved, err := profile.Merge(profiles, []string{"mainnet-base"}, nodeSpec)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if resolved.Consensus.Image != "sigp/lighthouse:v5.4.0" {
		t.Errorf("node override should win, got %s", resolved.Consensus.Image)
	}
	if resolved.Consensus.Client != "lighthouse" {
		t.Errorf("profile client should be preserved, got %s", resolved.Consensus.Client)
	}
}

func TestMerge_LaterProfileWinsOverEarlier(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "base", `
name: base
spec:
  execution:
    image: ethereum/client-go:v1.14.8
`)
	writeProfile(t, dir, "override", `
name: override
spec:
  execution:
    image: ethereum/client-go:v1.14.9
`)

	loader := profile.NewLoader(dir)
	profiles, _ := loader.Load()

	resolved, err := profile.Merge(profiles, []string{"base", "override"}, types.NodeSpec{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if resolved.Execution.Image != "ethereum/client-go:v1.14.9" {
		t.Errorf("later profile should win, got %s", resolved.Execution.Image)
	}
}

func TestMerge_PackagesMergeByName(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "base", `
name: base
spec:
  system:
    packages:
      - name: docker-ce
        version: "26.1.0"
      - name: curl
        version: latest
`)

	loader := profile.NewLoader(dir)
	profiles, _ := loader.Load()

	nodeSpec := types.NodeSpec{
		System: types.SystemSpec{
			Packages: []types.PackageSpec{
				{Name: "docker-ce", Version: "26.2.0"}, // upgrade docker only
			},
		},
	}

	resolved, err := profile.Merge(profiles, []string{"base"}, nodeSpec)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	pkgMap := make(map[string]string)
	for _, p := range resolved.System.Packages {
		pkgMap[p.Name] = p.Version
	}

	if pkgMap["docker-ce"] != "26.2.0" {
		t.Errorf("expected docker-ce 26.2.0, got %s", pkgMap["docker-ce"])
	}
	if pkgMap["curl"] != "latest" {
		t.Errorf("expected curl latest, got %s", pkgMap["curl"])
	}
}

func TestMerge_KernelParamsMerge(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "base", `
name: base
spec:
  system:
    kernel:
      parameters:
        vm.swappiness: "1"
        net.core.rmem_max: "134217728"
`)

	loader := profile.NewLoader(dir)
	profiles, _ := loader.Load()

	nodeSpec := types.NodeSpec{
		System: types.SystemSpec{
			Kernel: types.KernelSpec{
				Parameters: map[string]string{
					"vm.swappiness": "0", // override one param
				},
			},
		},
	}

	resolved, err := profile.Merge(profiles, []string{"base"}, nodeSpec)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if resolved.System.Kernel.Parameters["vm.swappiness"] != "0" {
		t.Errorf("node should override kernel param, got %s", resolved.System.Kernel.Parameters["vm.swappiness"])
	}
	if resolved.System.Kernel.Parameters["net.core.rmem_max"] != "134217728" {
		t.Errorf("profile param should be preserved, got %s", resolved.System.Kernel.Parameters["net.core.rmem_max"])
	}
}

func TestMerge_FirewallRulesMergeByPortAndProto(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "base", `
name: base
spec:
  network:
    firewall:
      provider: ufw
      policy: deny-by-default
      rules:
        - port: 30303
          proto: tcp
          direction: inbound
          action: allow
`)

	loader := profile.NewLoader(dir)
	profiles, _ := loader.Load()

	nodeSpec := types.NodeSpec{
		Network: types.NetworkSpec{
			Firewall: types.FirewallSpec{
				Rules: []types.FirewallRule{
					{Port: 9100, Proto: "tcp", Direction: "inbound", Action: "allow", Description: "node-exporter"},
				},
			},
		},
	}

	resolved, err := profile.Merge(profiles, []string{"base"}, nodeSpec)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if len(resolved.Network.Firewall.Rules) != 2 {
		t.Errorf("expected 2 firewall rules, got %d", len(resolved.Network.Firewall.Rules))
	}
}

func TestMerge_UnknownProfileReturnsError(t *testing.T) {
	dir := t.TempDir()
	loader := profile.NewLoader(dir)
	profiles, _ := loader.Load()

	_, err := profile.Merge(profiles, []string{"does-not-exist"}, types.NodeSpec{})
	if err == nil {
		t.Error("expected error for unknown profile")
	}
}

func TestMerge_MEVFromProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "mev-standard", `
name: mev-standard
spec:
  mev:
    enabled: true
    image: flashbots/mev-boost:v1.8.1
    listenAddr: "127.0.0.1:18550"
    relays:
      - url: https://relay.flashbots.net
        label: flashbots
        ofacFiltered: true
`)

	loader := profile.NewLoader(dir)
	profiles, _ := loader.Load()

	resolved, err := profile.Merge(profiles, []string{"mev-standard"}, types.NodeSpec{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if !resolved.MEV.Enabled {
		t.Error("expected MEV to be enabled")
	}
	if len(resolved.MEV.Relays) != 1 {
		t.Errorf("expected 1 relay, got %d", len(resolved.MEV.Relays))
	}
	if resolved.MEV.Relays[0].Label != "flashbots" {
		t.Errorf("expected flashbots relay, got %s", resolved.MEV.Relays[0].Label)
	}
}

func TestLoader_IgnoresNonYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0644)
	writeProfile(t, dir, "base", `name: base`)

	loader := profile.NewLoader(dir)
	profiles, err := loader.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("expected 1 profile, got %d", len(profiles))
	}
}
