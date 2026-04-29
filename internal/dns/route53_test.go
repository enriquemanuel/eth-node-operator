package dns_test

import (
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/dns"
)

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

		// Client passed as image string should strip to just the name
		{"bare-metal-05", "ethereum/client-go:v1.14.8", "validators.example.com",
			"bare-metal-05-client-go.validators.example.com"},

		// Case normalisation
		{"bare-metal-06", "Geth", "validators.example.com",
			"bare-metal-06-geth.validators.example.com"},

		{"bare-metal-07", "NETHERMIND", "validators.example.com",
			"bare-metal-07-nethermind.validators.example.com"},
	}

	for _, tc := range cases {
		t.Run(tc.node+"-"+tc.client, func(t *testing.T) {
			got := dns.Hostname(tc.node, tc.client, tc.zone)
			if got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestHostname_ConsistentForSameInputs(t *testing.T) {
	h1 := dns.Hostname("bare-metal-01", "geth", "validators.example.com")
	h2 := dns.Hostname("bare-metal-01", "geth", "validators.example.com")
	if h1 != h2 {
		t.Error("Hostname should be deterministic")
	}
}

func TestHostname_DifferentNodesProduceDifferentNames(t *testing.T) {
	h1 := dns.Hostname("bare-metal-01", "geth", "validators.example.com")
	h2 := dns.Hostname("bare-metal-02", "geth", "validators.example.com")
	if h1 == h2 {
		t.Error("Different nodes should produce different hostnames")
	}
}

func TestHostname_DifferentClientsProduceDifferentNames(t *testing.T) {
	h1 := dns.Hostname("bare-metal-01", "geth", "validators.example.com")
	h2 := dns.Hostname("bare-metal-01", "nethermind", "validators.example.com")
	if h1 == h2 {
		t.Error("Different clients should produce different hostnames")
	}
}

func TestIsConfigured_ReturnsBool(t *testing.T) {
	// Just verify it doesn't panic — the actual result depends on environment
	result := dns.IsConfigured()
	_ = result // true if aws CLI + credentials present, false otherwise
}
