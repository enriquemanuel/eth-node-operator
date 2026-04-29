package discover_test

import (
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/discover"
)

func TestImageToClientName(t *testing.T) {
	cases := []struct {
		image    string
		expected string
	}{
		{"ethereum/client-go:v1.14.8", "geth"},
		{"ethereum/client-go", "geth"},
		{"nethermind/nethermind:v1.26.0", "nethermind"},
		{"hyperledger/besu:24.10.0", "besu"},
		{"ghcr.io/paradigmxyz/reth:v1.1.0", "reth"},
		{"sigp/lighthouse:v5.3.0", "lighthouse"},
		{"consensys/teku:24.10.0", "teku"},
		{"gcr.io/prysmaticlabs/prysm/beacon-chain:v5.1.0", "prysm"},
	}

	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			got := discover.ImageToClientName(tc.image)
			if got != tc.expected {
				t.Errorf("ImageToClientName(%q) = %q, want %q", tc.image, got, tc.expected)
			}
		})
	}
}

func TestCgroupVersion(t *testing.T) {
	v := discover.CgroupVersion()
	if v != 1 && v != 2 {
		t.Errorf("CgroupVersion() = %d, want 1 or 2", v)
	}
}

func TestYAMLSnippet_ContainsExpectedSections(t *testing.T) {
	r := &discover.Report{
		PublicIP:      "203.0.113.1",
		CgroupVersion: 2,
		ELClient:      "geth",
		CLClient:      "lighthouse",
		VCGatewayIPs:  []string{"18.1.2.3", "52.4.5.6"},
		ManagementIPs: []string{"10.1.0.5"},
	}

	snippet := r.YAMLSnippet("bare-metal-01", "validators.example.com", "Z1234567890ABC")

	checks := []string{
		"bare-metal-01",
		"203.0.113.1",
		"18.1.2.3/32",
		"52.4.5.6/32",
		"10.1.0.5/32",
		"Z1234567890ABC",
		"validators.example.com",
		"cgroup v2",
		"cadvisor-cgroupv2",
	}

	for _, check := range checks {
		if !contains(snippet, check) {
			t.Errorf("YAML snippet missing %q\nGot:\n%s", check, snippet)
		}
	}
}

func TestYAMLSnippet_NoVCGateways_ShowsComment(t *testing.T) {
	r := &discover.Report{
		PublicIP:      "203.0.113.1",
		CgroupVersion: 1,
		ELClient:      "nethermind",
	}

	snippet := r.YAMLSnippet("bare-metal-02", "validators.example.com", "Z123")

	if !contains(snippet, "No :5052 connections detected") {
		t.Error("expected placeholder comment when no VC gateways detected")
	}
	if !contains(snippet, "cadvisor-cgroupv1") {
		t.Error("expected cgroup v1 compose command")
	}
}

func TestYAMLSnippet_WithWarnings(t *testing.T) {
	r := &discover.Report{
		PublicIP:      "203.0.113.1",
		CgroupVersion: 2,
		Warnings:      []string{"EL client: container 'execution' not running"},
	}

	snippet := r.YAMLSnippet("bare-metal-03", "validators.example.com", "Z123")

	if !contains(snippet, "container 'execution' not running") {
		t.Error("expected warning in YAML snippet")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 &&
		func() bool {
			for i := range s {
				if i+len(substr) <= len(s) && s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
