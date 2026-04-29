// Package dns manages public DNS A-record registration for bare metal nodes.
//
// On startup the agent calls Register() which:
//  1. Detects the node's public IP
//  2. Derives the hostname: {nodeName}-{elClient}.{zone}
//  3. Upserts an A record in Route 53 via the aws CLI
//
// Using the aws CLI avoids implementing SigV4 request signing.
// AWS credentials are read from the standard locations:
//   - IAM instance profile (if the OVH node has one — unlikely)
//   - AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY environment variables
//   - ~/.aws/credentials
//
// The IAM policy required is minimal:
//
//	{
//	  "Effect": "Allow",
//	  "Action": ["route53:ChangeResourceRecordSets", "route53:ListResourceRecordSets"],
//	  "Resource": "arn:aws:route53:::hostedzone/{ZONE_ID}"
//	}
package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Manager handles Route 53 A-record registration.
type Manager struct {
	http *http.Client
}

// New returns a Manager.
func New() *Manager {
	return &Manager{http: &http.Client{Timeout: 10 * time.Second}}
}

// RegisterResult describes what was registered.
type RegisterResult struct {
	Hostname string
	IP       string
	ZoneID   string
	Action   string // UPSERT / already-current
}

// Register ensures an A record exists for this node in Route 53.
// hostname is the full FQDN: {nodeName}-{elClient}.{zone}
func (m *Manager) Register(ctx context.Context, hostname, zoneID string, ttl int) (*RegisterResult, error) {
	if ttl == 0 {
		ttl = 300
	}

	// 1. Get public IP
	ip, err := m.publicIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("get public IP: %w", err)
	}

	// 2. Check if record is already current
	current, err := m.currentRecord(ctx, zoneID, hostname)
	if err == nil && current == ip {
		return &RegisterResult{
			Hostname: hostname,
			IP:       ip,
			ZoneID:   zoneID,
			Action:   "already-current",
		}, nil
	}

	// 3. Upsert A record via aws CLI
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
		return nil, fmt.Errorf("aws route53 upsert %s → %s: %s: %w", hostname, ip, string(out), err)
	}

	return &RegisterResult{
		Hostname: hostname,
		IP:       ip,
		ZoneID:   zoneID,
		Action:   "UPSERT",
	}, nil
}

// Hostname derives the node hostname from the node name and EL client.
// Format: {nodeName}-{elClient}.{zone}
// Example: "bare-metal-01", "geth", "validators.example.com"
//       → "bare-metal-01-geth.validators.example.com"
func Hostname(nodeName, elClient, zone string) string {
	// Normalise client name to lowercase slug
	client := strings.ToLower(elClient)
	// Strip any image tag if client was passed as an image string
	if idx := strings.LastIndex(client, "/"); idx >= 0 {
		client = client[idx+1:]
	}
	if idx := strings.Index(client, ":"); idx >= 0 {
		client = client[:idx]
	}
	return fmt.Sprintf("%s-%s.%s", nodeName, client, zone)
}

// publicIP returns this node's public IPv4 address.
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

// currentRecord returns the current A record value, or error if not found.
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

// IsConfigured returns true if the aws CLI is installed and credentials are present.
func IsConfigured() bool {
	if _, err := exec.LookPath("aws"); err != nil {
		return false
	}
	// Check for credentials in env or default profile
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		return true
	}
	home, _ := os.UserHomeDir()
	_, err := os.Stat(home + "/.aws/credentials")
	return err == nil
}

// --- JSON helpers for testing ---

type route53ListResponse struct {
	ResourceRecordSets []struct {
		Name            string `json:"Name"`
		Type            string `json:"Type"`
		ResourceRecords []struct {
			Value string `json:"Value"`
		} `json:"ResourceRecords"`
	} `json:"ResourceRecordSets"`
}

func parseListResponse(data []byte) (string, error) {
	var resp route53ListResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	for _, rr := range resp.ResourceRecordSets {
		if rr.Type == "A" && len(rr.ResourceRecords) > 0 {
			return rr.ResourceRecords[0].Value, nil
		}
	}
	return "", fmt.Errorf("no A record found")
}
