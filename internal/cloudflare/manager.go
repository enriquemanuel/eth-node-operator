// Package cloudflare manages Cloudflare DNS records and Tunnel for bare metal nodes.
//
// On each node the agent:
//  1. Creates/updates an A record: {nodeSubdomain}.{domain} → node's public IP
//  2. Provisions a Cloudflare Tunnel (outbound-only, no inbound ports)
//  3. Creates a CNAME: {clSubdomain}.{domain} → {tunnel}.cfargotunnel.com
//  4. Writes /etc/cloudflared/config.yml pointing to localhost:5052
//  5. Ensures cloudflared.service is running
//
// No inbound port 443 required. The tunnel is purely outbound (UDP 7844).
// Access control is handled by Cloudflare Access policies, not IP allowlists.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const cfAPIBase = "https://api.cloudflare.com/client/v4"

// Manager handles Cloudflare DNS and Tunnel provisioning for a node.
type Manager struct {
	apiToken  string
	http      *http.Client
}

// New returns a Manager using the given Cloudflare API token.
func New(apiToken string) *Manager {
	return &Manager{
		apiToken: apiToken,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// NodeConfig describes the DNS and tunnel configuration for one node.
type NodeConfig struct {
	AccountID     string
	ZoneID        string
	Domain        string
	NodeSubdomain string // A record: {NodeSubdomain}.{Domain}
	CLSubdomain   string // CNAME: {CLSubdomain}.{Domain} → tunnel
	TunnelName    string
	CLListenAddr  string // default: http://localhost:5052
}

// ReconcileResult describes what changed.
type ReconcileResult struct {
	NodeDNS    string // the A record FQDN
	CLFQDN     string // the CL CNAME FQDN
	TunnelID   string
	Actions    []string
}

// Reconcile ensures the A record, tunnel, and CL CNAME are all correct.
// This is idempotent — safe to call on every reconcile loop.
func (m *Manager) Reconcile(ctx context.Context, cfg NodeConfig) (*ReconcileResult, error) {
	if cfg.CLListenAddr == "" {
		cfg.CLListenAddr = "http://localhost:5052"
	}

	result := &ReconcileResult{
		NodeDNS: fmt.Sprintf("%s.%s", cfg.NodeSubdomain, cfg.Domain),
		CLFQDN:  fmt.Sprintf("%s.%s", cfg.CLSubdomain, cfg.Domain),
	}

	// 1. Node A record → public IP
	ip, err := m.publicIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("get public IP: %w", err)
	}

	if err := m.upsertARecord(ctx, cfg.ZoneID, result.NodeDNS, ip); err != nil {
		return nil, fmt.Errorf("upsert A record %s: %w", result.NodeDNS, err)
	}
	result.Actions = append(result.Actions, fmt.Sprintf("dns: A %s → %s", result.NodeDNS, ip))

	// 2. Provision tunnel
	tunnelID, tunnelHostname, created, err := m.ensureTunnel(ctx, cfg.AccountID, cfg.TunnelName)
	if err != nil {
		return nil, fmt.Errorf("ensure tunnel %s: %w", cfg.TunnelName, err)
	}
	result.TunnelID = tunnelID
	if created {
		result.Actions = append(result.Actions, fmt.Sprintf("tunnel: created %s (%s)", cfg.TunnelName, tunnelID))
	}

	// 3. CL CNAME → tunnel hostname
	if err := m.upsertCNAME(ctx, cfg.ZoneID, result.CLFQDN, tunnelHostname); err != nil {
		return nil, fmt.Errorf("upsert CNAME %s: %w", result.CLFQDN, err)
	}
	result.Actions = append(result.Actions, fmt.Sprintf("dns: CNAME %s → %s", result.CLFQDN, tunnelHostname))

	// 4. Write cloudflared config
	if err := m.writeCloudflaredConfig(tunnelID, result.CLFQDN, cfg.CLListenAddr); err != nil {
		return nil, fmt.Errorf("write cloudflared config: %w", err)
	}

	// 5. Ensure cloudflared is running
	if restarted, err := m.ensureCloudflared(ctx); err != nil {
		return nil, fmt.Errorf("ensure cloudflared: %w", err)
	} else if restarted {
		result.Actions = append(result.Actions, "cloudflared: (re)started")
	}

	return result, nil
}

// --- DNS ---------------------------------------------------------------

func (m *Manager) upsertARecord(ctx context.Context, zoneID, fqdn, ip string) error {
	return m.upsertRecord(ctx, zoneID, fqdn, "A", ip, false)
}

func (m *Manager) upsertCNAME(ctx context.Context, zoneID, fqdn, target string) error {
	// Cloudflare-proxied CNAME: traffic goes through CF edge (Zero Trust / Access apply)
	return m.upsertRecord(ctx, zoneID, fqdn, "CNAME", target, true)
}

type cfRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

type cfListResponse struct {
	Result []cfRecord `json:"result"`
	Success bool      `json:"success"`
}

type cfSingleResponse struct {
	Result cfRecord `json:"result"`
	Success bool    `json:"success"`
}

func (m *Manager) upsertRecord(ctx context.Context, zoneID, fqdn, recordType, content string, proxied bool) error {
	// Check if record exists
	existing, err := m.findRecord(ctx, zoneID, fqdn, recordType)
	if err != nil {
		return err
	}

	record := cfRecord{
		Name:    fqdn,
		Type:    recordType,
		Content: content,
		Proxied: proxied,
		TTL:     1, // 1 = auto TTL
	}

	if existing != nil {
		// Update if content changed
		if existing.Content == content && existing.Proxied == proxied {
			return nil // already correct
		}
		_, err = m.cfRequest(ctx, "PUT",
			fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, existing.ID),
			record, nil)
		return err
	}

	// Create
	_, err = m.cfRequest(ctx, "POST",
		fmt.Sprintf("/zones/%s/dns_records", zoneID),
		record, nil)
	return err
}

func (m *Manager) findRecord(ctx context.Context, zoneID, name, recordType string) (*cfRecord, error) {
	path := fmt.Sprintf("/zones/%s/dns_records?name=%s&type=%s", zoneID, name, recordType)
	body, err := m.cfRequest(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	var resp cfListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Result) == 0 {
		return nil, nil
	}
	return &resp.Result[0], nil
}

// --- Tunnel ------------------------------------------------------------

type cfTunnel struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	RemoteConfig      bool   `json:"remote_config"`
	Status            string `json:"status"`
}

type cfTunnelListResponse struct {
	Result []cfTunnel `json:"result"`
}

func (m *Manager) ensureTunnel(ctx context.Context, accountID, name string) (tunnelID, hostname string, created bool, err error) {
	// List existing tunnels
	body, err := m.cfRequest(ctx, "GET",
		fmt.Sprintf("/accounts/%s/cfd_tunnel?name=%s&is_deleted=false", accountID, name),
		nil, nil)
	if err != nil {
		return "", "", false, err
	}

	var list cfTunnelListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return "", "", false, err
	}

	if len(list.Result) > 0 {
		t := list.Result[0]
		return t.ID, fmt.Sprintf("%s.cfargotunnel.com", t.ID), false, nil
	}

	// Create tunnel
	type createReq struct {
		Name         string `json:"name"`
		ConfigSrc    string `json:"config_src"`
	}
	body, err = m.cfRequest(ctx, "POST",
		fmt.Sprintf("/accounts/%s/cfd_tunnel", accountID),
		createReq{Name: name, ConfigSrc: "local"},
		nil)
	if err != nil {
		return "", "", false, fmt.Errorf("create tunnel: %w", err)
	}

	var resp struct {
		Result cfTunnel `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", false, err
	}

	return resp.Result.ID, fmt.Sprintf("%s.cfargotunnel.com", resp.Result.ID), true, nil
}

// --- cloudflared config ------------------------------------------------

func (m *Manager) writeCloudflaredConfig(tunnelID, hostname, clAddr string) error {
	const configDir = "/etc/cloudflared"
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

	config := fmt.Sprintf(`tunnel: %s
credentials-file: /etc/cloudflared/%s.json

ingress:
  # Beacon node HTTP API — only accessible via Cloudflare Access policy
  - hostname: %s
    service: %s
    originRequest:
      noTLSVerify: false
      connectTimeout: 30s

  # Catch-all: reject everything else
  - service: http_status:404
`, tunnelID, tunnelID, hostname, clAddr)

	return os.WriteFile(filepath.Join(configDir, "config.yml"), []byte(config), 0640)
}

// --- cloudflared process -----------------------------------------------

func (m *Manager) ensureCloudflared(ctx context.Context) (bool, error) {
	// Check if cloudflared is installed
	if _, err := exec.LookPath("cloudflared"); err != nil {
		return false, fmt.Errorf("cloudflared not installed — add it to system packages")
	}

	// Check if systemd unit is running
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", "cloudflared").Output()
	if strings.TrimSpace(string(out)) == "active" {
		// Config might have changed — reload
		exec.CommandContext(ctx, "systemctl", "reload-or-restart", "cloudflared").Run() //nolint
		return false, nil
	}

	// Enable and start
	exec.CommandContext(ctx, "systemctl", "enable", "cloudflared").Run() //nolint
	if err := exec.CommandContext(ctx, "systemctl", "start", "cloudflared").Run(); err != nil {
		return false, fmt.Errorf("start cloudflared: %w", err)
	}
	return true, nil
}

// --- Helpers -----------------------------------------------------------

func (m *Manager) publicIP(ctx context.Context) (string, error) {
	// Try multiple providers for resilience
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
		if net.ParseIP(ip) != nil {
			return ip, nil
		}
	}
	return "", fmt.Errorf("could not determine public IP")
}

func (m *Manager) cfRequest(ctx context.Context, method, path string, body interface{}, out interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, cfAPIBase+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CF API %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("CF API %s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}

	if out != nil {
		return nil, json.Unmarshal(respBody, out)
	}
	return respBody, nil
}
