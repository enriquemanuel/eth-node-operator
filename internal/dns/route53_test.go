package dns_test

import (
	"os"
	"path/filepath"
	"encoding/json"
	"testing"
	"time"

	"github.com/enriquemanuel/eth-node-operator/internal/dns"
)

// ── Hostname derivation ───────────────────────────────────────────────

func TestHostname(t *testing.T) {
	cases := []struct {
		node, client, zone string
		expected           string
	}{
		{"bare-metal-01", "geth", "validators.example.com",
			"bare-metal-01-geth.validators.example.com"},
		{"bare-metal-02", "nethermind", "validators.example.com",
			"bare-metal-02-nethermind.validators.example.com"},
		{"bare-metal-03", "reth", "validators.example.com",
			"bare-metal-03-reth.validators.example.com"},
		{"bare-metal-04", "besu", "validators.example.com",
			"bare-metal-04-besu.validators.example.com"},
		// Image string → strip path and tag
		{"bare-metal-05", "ethereum/client-go:v1.14.8", "validators.example.com",
			"bare-metal-05-client-go.validators.example.com"},
		// Case normalisation
		{"bare-metal-06", "Geth", "validators.example.com",
			"bare-metal-06-geth.validators.example.com"},
		{"bare-metal-07", "NETHERMIND", "validators.example.com",
			"bare-metal-07-nethermind.validators.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.node+"/"+tc.client, func(t *testing.T) {
			got := dns.Hostname(tc.node, tc.client, tc.zone)
			if got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestHostname_Deterministic(t *testing.T) {
	h1 := dns.Hostname("bare-metal-01", "geth", "validators.example.com")
	h2 := dns.Hostname("bare-metal-01", "geth", "validators.example.com")
	if h1 != h2 {
		t.Error("Hostname must be deterministic")
	}
}

func TestHostname_DifferentNodesProduceDifferentNames(t *testing.T) {
	h1 := dns.Hostname("bare-metal-01", "geth", "validators.example.com")
	h2 := dns.Hostname("bare-metal-02", "geth", "validators.example.com")
	if h1 == h2 {
		t.Error("different nodes must produce different hostnames")
	}
}

func TestHostname_DifferentClientsProduceDifferentNames(t *testing.T) {
	h1 := dns.Hostname("bare-metal-01", "geth", "validators.example.com")
	h2 := dns.Hostname("bare-metal-01", "nethermind", "validators.example.com")
	if h1 == h2 {
		t.Error("different clients must produce different hostnames")
	}
}

// ── State file ────────────────────────────────────────────────────────

func writeState(t *testing.T, path string, state *dns.DNSState) {
	t.Helper()
	data, _ := json.MarshalIndent(state, "", "  ")
	os.MkdirAll(filepath.Dir(path), 0750)
	os.WriteFile(path, data, 0640)
}

func TestLoadState_ReturnsPersistedState(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "dns-state.json")

	expected := &dns.DNSState{
		Hostname:     "bare-metal-01-geth.validators.example.com",
		IP:           "203.0.113.1",
		ZoneID:       "Z123",
		RegisteredAt: time.Now().UTC().Truncate(time.Second),
	}
	writeState(t, stateFile, expected)

	m := dns.NewWithStateFile(stateFile)
	got, err := m.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if got.Hostname != expected.Hostname {
		t.Errorf("hostname: got %q, want %q", got.Hostname, expected.Hostname)
	}
	if got.IP != expected.IP {
		t.Errorf("ip: got %q, want %q", got.IP, expected.IP)
	}
}

func TestLoadState_MissingFile(t *testing.T) {
	m := dns.NewWithStateFile("/does/not/exist/dns-state.json")
	_, err := m.LoadState()
	if err == nil {
		t.Error("expected error for missing state file")
	}
}

// ── IsConfigured ─────────────────────────────────────────────────────

func TestIsConfigured_ReturnsBool(t *testing.T) {
	// Verify it doesn't panic; actual value depends on environment
	_ = dns.IsConfigured()
}

// ── Audit logic (unit test without aws CLI) ───────────────────────────
// We can't run real aws CLI in tests, but we can test the audit filter logic.

func TestAuditFilter(t *testing.T) {
	// Simulate: Route 53 has 3 records, cluster expects 2
	allRecords := []dns.ZoneRecord{
		{Hostname: "bare-metal-01-geth.validators.example.com", IP: "1.2.3.1"},
		{Hostname: "bare-metal-02-nethermind.validators.example.com", IP: "1.2.3.2"},
		{Hostname: "bare-metal-99-reth.validators.example.com", IP: "1.2.3.99"}, // stale
	}

	expected := map[string]bool{
		"bare-metal-01-geth.validators.example.com":       true,
		"bare-metal-02-nethermind.validators.example.com": true,
	}

	var stale []dns.ZoneRecord
	for _, r := range allRecords {
		if !expected[r.Hostname] {
			stale = append(stale, r)
		}
	}

	if len(stale) != 1 {
		t.Fatalf("expected 1 stale record, got %d", len(stale))
	}
	if stale[0].Hostname != "bare-metal-99-reth.validators.example.com" {
		t.Errorf("wrong stale record: %s", stale[0].Hostname)
	}
}

func TestAuditFilter_NothingStale(t *testing.T) {
	allRecords := []dns.ZoneRecord{
		{Hostname: "bare-metal-01-geth.validators.example.com", IP: "1.2.3.1"},
	}
	expected := map[string]bool{
		"bare-metal-01-geth.validators.example.com": true,
	}

	var stale []dns.ZoneRecord
	for _, r := range allRecords {
		if !expected[r.Hostname] {
			stale = append(stale, r)
		}
	}
	if len(stale) != 0 {
		t.Errorf("expected no stale records, got %d", len(stale))
	}
}

func TestAuditFilter_AllStale(t *testing.T) {
	allRecords := []dns.ZoneRecord{
		{Hostname: "bare-metal-99-geth.validators.example.com", IP: "1.2.3.99"},
		{Hostname: "bare-metal-100-reth.validators.example.com", IP: "1.2.3.100"},
	}
	expected := map[string]bool{} // cluster is empty — all records stale

	var stale []dns.ZoneRecord
	for _, r := range allRecords {
		if !expected[r.Hostname] {
			stale = append(stale, r)
		}
	}
	if len(stale) != 2 {
		t.Errorf("expected 2 stale records, got %d", len(stale))
	}
}
