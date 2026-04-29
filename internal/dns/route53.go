// Package dns manages public DNS A-record registration for bare metal nodes.
//
// On startup the agent calls Register() which:
//  1. Detects the node's public IP
//  2. Derives the hostname: {nodeName}-{elClient}.{zone}
//  3. Checks local state file for previously registered hostname
//  4. If hostname changed (client swap), deletes the old A record first
//  5. Upserts the new A record in Route 53 via the aws CLI
//  6. Persists the registered hostname to local state file
//
// Using the aws CLI avoids implementing SigV4 request signing.
// AWS credentials: AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY in env,
// or an IAM role attached to the host.
package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const stateFile = "/etc/ethagent/dns-state.json"

// Manager handles Route 53 A-record registration.
type Manager struct {
	http      *http.Client
	stateFile string // override for testing
}

// New returns a Manager.
func New() *Manager {
	return &Manager{
		http:      &http.Client{Timeout: 10 * time.Second},
		stateFile: stateFile,
	}
}

// NewWithStateFile returns a Manager with a custom state file path (for testing).
func NewWithStateFile(path string) *Manager {
	return &Manager{
		http:      &http.Client{Timeout: 10 * time.Second},
		stateFile: path,
	}
}

// DNSState is persisted to disk to track what record this node has registered.
type DNSState struct {
	Hostname     string    `json:"hostname"`
	IP           string    `json:"ip"`
	ZoneID       string    `json:"zoneId"`
	RegisteredAt time.Time `json:"registeredAt"`
}

// RegisterResult describes what was registered.
type RegisterResult struct {
	Hostname    string
	IP          string
	ZoneID      string
	Action      string // "registered" | "updated-ip" | "changed-client" | "already-current"
	OldHostname string // set when client changed and old record was deleted
}

// Register ensures the correct A record exists and the state file is current.
// Handles: IP changes, client swaps (old record deleted), idempotent re-runs.
func (m *Manager) Register(ctx context.Context, hostname, zoneID string, ttl int) (*RegisterResult, error) {
	if ttl == 0 {
		ttl = 300
	}

	ip, err := m.publicIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("get public IP: %w", err)
	}

	result := &RegisterResult{Hostname: hostname, IP: ip, ZoneID: zoneID}

	// Load previous state — detect client changes
	prev, _ := m.loadState()
	if prev != nil && prev.Hostname != hostname && prev.ZoneID == zoneID {
		// Client changed (e.g. geth → nethermind): delete the old record
		if err := m.Delete(ctx, prev.Hostname, zoneID); err != nil {
			// Non-fatal: log it but proceed with registration
			result.OldHostname = prev.Hostname + " (delete failed: " + err.Error() + ")"
		} else {
			result.OldHostname = prev.Hostname
			result.Action = "changed-client"
		}
	}

	// Check if A record is already current
	current, err := m.currentRecord(ctx, zoneID, hostname)
	if err == nil && current == ip {
		result.Action = "already-current"
		return result, nil
	}

	// Upsert A record
	if err := m.upsert(ctx, hostname, zoneID, ip, ttl); err != nil {
		return nil, err
	}

	if result.Action == "" {
		if current != "" {
			result.Action = "updated-ip"
		} else {
			result.Action = "registered"
		}
	}

	// Persist state
	m.saveState(&DNSState{
		Hostname:     hostname,
		IP:           ip,
		ZoneID:       zoneID,
		RegisteredAt: time.Now().UTC(),
	})

	return result, nil
}

// Delete removes an A record from Route 53.
func (m *Manager) Delete(ctx context.Context, hostname, zoneID string) error {
	ip, err := m.currentRecord(ctx, zoneID, hostname)
	if err != nil {
		return nil // record doesn't exist, nothing to delete
	}

	changeBatch := fmt.Sprintf(`{
  "Changes": [{
    "Action": "DELETE",
    "ResourceRecordSet": {
      "Name": "%s",
      "Type": "A",
      "TTL": 300,
      "ResourceRecords": [{"Value": "%s"}]
    }
  }]
}`, hostname, ip)

	cmd := exec.CommandContext(ctx, "aws", "route53",
		"change-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--change-batch", changeBatch,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("delete %s: %s: %w", hostname, string(out), err)
	}
	return nil
}

// ListZoneRecords returns all A records in the zone matching the given suffix.
// suffix is typically the base zone, e.g. "validators.example.com"
func (m *Manager) ListZoneRecords(ctx context.Context, zoneID, suffix string) ([]ZoneRecord, error) {
	cmd := exec.CommandContext(ctx, "aws", "route53",
		"list-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--output", "json",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list records in zone %s: %w", zoneID, err)
	}

	type awsRecord struct {
		Name            string `json:"Name"`
		Type            string `json:"Type"`
		TTL             int    `json:"TTL"`
		ResourceRecords []struct {
			Value string `json:"Value"`
		} `json:"ResourceRecords"`
	}
	var resp struct {
		ResourceRecordSets []awsRecord `json:"ResourceRecordSets"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse list response: %w", err)
	}

	var records []ZoneRecord
	for _, r := range resp.ResourceRecordSets {
		if r.Type != "A" {
			continue
		}
		// Route 53 returns names with trailing dot
		name := strings.TrimSuffix(r.Name, ".")
		if suffix != "" && !strings.HasSuffix(name, suffix) {
			continue
		}
		ip := ""
		if len(r.ResourceRecords) > 0 {
			ip = r.ResourceRecords[0].Value
		}
		records = append(records, ZoneRecord{
			Hostname: name,
			IP:       ip,
			TTL:      r.TTL,
		})
	}
	return records, nil
}

// ZoneRecord is a single A record in a Route 53 zone.
type ZoneRecord struct {
	Hostname string
	IP       string
	TTL      int
}

// Audit compares Route 53 records against a set of expected hostnames.
// Returns records present in Route 53 that are NOT in expectedHostnames —
// i.e. stale records that should be cleaned up.
func (m *Manager) Audit(ctx context.Context, zoneID, zone string, expectedHostnames map[string]bool) ([]ZoneRecord, error) {
	records, err := m.ListZoneRecords(ctx, zoneID, zone)
	if err != nil {
		return nil, err
	}

	var stale []ZoneRecord
	for _, r := range records {
		if !expectedHostnames[r.Hostname] {
			stale = append(stale, r)
		}
	}
	return stale, nil
}

// Hostname derives the node A-record hostname from node name and EL client.
// Format: {nodeName}-{elClient}.{zone}
// Examples:
//   "bare-metal-01", "geth", "validators.example.com"
//     → "bare-metal-01-geth.validators.example.com"
//   "bare-metal-02", "nethermind", "validators.example.com"
//     → "bare-metal-02-nethermind.validators.example.com"
func Hostname(nodeName, elClient, zone string) string {
	client := strings.ToLower(elClient)
	// Strip image path prefix (e.g. "ethereum/client-go" → "client-go")
	if idx := strings.LastIndex(client, "/"); idx >= 0 {
		client = client[idx+1:]
	}
	// Strip image tag (e.g. "client-go:v1.14.8" → "client-go")
	if idx := strings.Index(client, ":"); idx >= 0 {
		client = client[:idx]
	}
	return fmt.Sprintf("%s-%s.%s", nodeName, client, zone)
}

// IsConfigured returns true if the aws CLI and credentials are available.
func IsConfigured() bool {
	if _, err := exec.LookPath("aws"); err != nil {
		return false
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		return true
	}
	home, _ := os.UserHomeDir()
	_, err := os.Stat(home + "/.aws/credentials")
	return err == nil
}

// LoadState returns the persisted DNS registration state for this node.
func (m *Manager) LoadState() (*DNSState, error) {
	return m.loadState()
}

// --- internals ---

func (m *Manager) upsert(ctx context.Context, hostname, zoneID, ip string, ttl int) error {
	changeBatch := fmt.Sprintf(`{
  "Comment": "eth-node-operator auto-registration",
  "Changes": [{
    "Action": "UPSERT",
    "ResourceRecordSet": {
      "Name": "%s",
      "Type": "A",
      "TTL": %d,
      "ResourceRecords": [{"Value": "%s"}]
    }
  }]
}`, hostname, ttl, ip)

	cmd := exec.CommandContext(ctx, "aws", "route53",
		"change-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--change-batch", changeBatch,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("aws route53 upsert %s → %s: %s: %w", hostname, ip, string(out), err)
	}
	return nil
}

func (m *Manager) currentRecord(ctx context.Context, zoneID, hostname string) (string, error) {
	cmd := exec.CommandContext(ctx, "aws", "route53",
		"list-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--query", fmt.Sprintf(`ResourceRecordSets[?Name=='%s.' && Type=='A'].ResourceRecords[0].Value`, hostname),
		"--output", "text",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	val := strings.TrimSpace(string(out))
	if val == "" || val == "None" {
		return "", fmt.Errorf("no record found")
	}
	return val, nil
}

func (m *Manager) publicIP(ctx context.Context) (string, error) {
	for _, url := range []string{
		"https://api.ipify.org",
		"https://ipv4.icanhazip.com",
	} {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			continue
		}
		resp, err := m.http.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ip := strings.TrimSpace(string(body))
		if ip != "" && !strings.Contains(ip, " ") {
			return ip, nil
		}
	}
	return "", fmt.Errorf("could not determine public IP")
}

func (m *Manager) loadState() (*DNSState, error) {
	data, err := os.ReadFile(m.stateFile)
	if err != nil {
		return nil, err
	}
	var state DNSState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (m *Manager) saveState(state *DNSState) {
	data, _ := json.MarshalIndent(state, "", "  ")
	dir := filepath.Dir(m.stateFile)
	os.MkdirAll(dir, 0750) //nolint:errcheck
	os.WriteFile(m.stateFile, data, 0600) //nolint:errcheck
}
