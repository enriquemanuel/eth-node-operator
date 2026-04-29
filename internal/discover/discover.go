// Package discover probes the local node environment to detect configuration
// values that would otherwise need to be set manually in the cluster file.
//
// Designed to run once at bootstrap via 'ethctl discover <node>', not
// continuously — auto-updating firewall rules from observed connections
// would create dangerous feedback loops (VC disconnects → CIDR removed → VC
// can't reconnect).
package discover

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// Report contains everything the agent was able to detect about this node.
type Report struct {
	// PublicIP is the node's internet-facing IPv4 address.
	PublicIP string `json:"publicIP"`

	// CgroupVersion is 1 or 2. Determines whether cAdvisor needs privileged mode.
	CgroupVersion int `json:"cgroupVersion"`

	// ELClient is the running execution layer client name (geth, nethermind, etc.)
	// Derived from the "execution" container image. Empty if not running.
	ELClient string `json:"elClient"`

	// CLClient is the running consensus layer client name (lighthouse, teku, etc.)
	// Derived from the "consensus" container image. Empty if not running.
	CLClient string `json:"clClient"`

	// VCGatewayIPs are the IPs currently connected to port 5052.
	// These are your EKS NAT gateway IPs — the Lighthouse VC source addresses.
	// Only populated if the VC is currently connected.
	VCGatewayIPs []string `json:"vcGatewayIPs"`

	// ManagementIPs are the source IPs of active SSH sessions.
	// These are candidates for managementCIDRs (your bastion/jump host IPs).
	ManagementIPs []string `json:"managementIPs"`

	// Warnings lists non-fatal discovery issues.
	Warnings []string `json:"warnings,omitempty"`
}

// Run performs all discovery probes and returns a Report.
// Individual probe failures are recorded in Report.Warnings, not returned as errors.
func Run(ctx context.Context) *Report {
	r := &Report{}

	// Public IP
	if ip, err := publicIP(ctx); err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("public IP: %v", err))
	} else {
		r.PublicIP = ip
	}

	// cgroup version
	r.CgroupVersion = CgroupVersion()

	// Running client names from Docker
	if client, err := runningClientName(ctx, "execution"); err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("EL client: %v", err))
	} else {
		r.ELClient = client
	}

	if client, err := runningClientName(ctx, "consensus"); err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("CL client: %v", err))
	} else {
		r.CLClient = client
	}

	// VC gateway IPs — who is connected to :5052
	if ips, err := connectedSourceIPs(ctx, "5052"); err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("VC gateways: %v (is the VC connected?)", err))
	} else {
		r.VCGatewayIPs = ips
	}

	// Management IPs — active SSH sessions
	if ips, err := connectedSourceIPs(ctx, "22"); err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("management IPs: %v", err))
	} else {
		r.ManagementIPs = ips
	}

	return r
}

// YAMLSnippet returns a YAML block ready to paste into the cluster node spec.
// Fields that couldn't be detected are left as comments.
func (r *Report) YAMLSnippet(nodeName, zone, zoneID string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("# Auto-discovered configuration for node: %s\n", nodeName))
	b.WriteString("# Paste this under the node's spec: block in your cluster file.\n")
	b.WriteString("# Review before applying — especially CIDRs.\n\n")

	b.WriteString("spec:\n")

	// DNS
	if r.PublicIP != "" {
		b.WriteString("  # Route 53 will register this A record:\n")
		if r.ELClient != "" && zone != "" {
			hostname := fmt.Sprintf("%s-%s.%s", nodeName, r.ELClient, zone)
			b.WriteString(fmt.Sprintf("  #   %s → %s\n", hostname, r.PublicIP))
		}
		b.WriteString("  network:\n")
		if zoneID != "" {
			b.WriteString("    route53:\n")
			b.WriteString(fmt.Sprintf("      zoneId: %s\n", zoneID))
			if zone != "" {
				b.WriteString(fmt.Sprintf("      zone: %s\n", zone))
			}
		}

		// VC gateways
		if len(r.VCGatewayIPs) > 0 {
			b.WriteString("    vcGateways:   # EKS NAT gateway IPs currently connected to :5052\n")
			for _, ip := range r.VCGatewayIPs {
				b.WriteString(fmt.Sprintf("      - \"%s/32\"\n", ip))
			}
		} else {
			b.WriteString("    vcGateways:   # No :5052 connections detected — add manually\n")
			b.WriteString("      # - \"18.x.x.x/32\"\n")
		}

		// Management CIDRs
		if len(r.ManagementIPs) > 0 {
			b.WriteString("    managementCIDRs:   # Active SSH session source IPs\n")
			for _, ip := range r.ManagementIPs {
				b.WriteString(fmt.Sprintf("      - \"%s/32\"\n", ip))
			}
		} else {
			b.WriteString("    managementCIDRs:   # No SSH sessions detected — add manually\n")
			b.WriteString("      # - \"10.x.x.x/32\"\n")
		}
	}

	// cAdvisor compose override
	if r.CgroupVersion > 0 {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("# cgroup v%d detected\n", r.CgroupVersion))
		if r.CgroupVersion == 2 {
			b.WriteString("# Deploy with: docker compose -f docker-compose.observability.yml \\\n")
			b.WriteString("#   -f docker-compose.cadvisor-cgroupv2.yml up -d\n")
			b.WriteString("# (no privileged mode needed for cAdvisor)\n")
		} else {
			b.WriteString("# Deploy with: docker compose -f docker-compose.observability.yml \\\n")
			b.WriteString("#   -f docker-compose.cadvisor-cgroupv1.yml up -d\n")
			b.WriteString("# (privileged: true required for cAdvisor on cgroup v1)\n")
		}
	}

	if len(r.Warnings) > 0 {
		b.WriteString("\n# Warnings:\n")
		for _, w := range r.Warnings {
			b.WriteString(fmt.Sprintf("#   %s\n", w))
		}
	}

	return b.String()
}

// --- probes ---

func publicIP(ctx context.Context) (string, error) {
	for _, url := range []string{"https://api.ipify.org", "https://ipv4.icanhazip.com"} {
		cmd := exec.CommandContext(ctx, "curl", "-sf", "--max-time", "5", url)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		ip := strings.TrimSpace(string(out))
		if net.ParseIP(ip) != nil {
			return ip, nil
		}
	}
	return "", fmt.Errorf("could not reach any IP detection service")
}

func CgroupVersion() int {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return 2
	}
	return 1
}

// runningClientName inspects a Docker container and extracts the client name
// from its image tag. Container "execution" → "ethereum/client-go:v1.14.8" → "geth".
func runningClientName(ctx context.Context, containerName string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "inspect",
		containerName,
		"--format", "{{.Config.Image}}",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("container %q not running", containerName)
	}
	image := strings.TrimSpace(string(out))
	return ImageToClientName(image), nil
}

// imageToClientName maps Docker image names to human client names.
// ImageToClientName maps a Docker image string to a human client name.
// Matches against the full image path so multi-segment paths like
// gcr.io/prysmaticlabs/prysm/beacon-chain:v5.1.0 resolve correctly.
func ImageToClientName(image string) string {
	full := strings.ToLower(image)
	// Strip tag for matching
	slug := full
	if idx := strings.Index(slug, ":"); idx >= 0 {
		slug = slug[:idx]
	}
	// Match against the full path — order matters for overlapping names
	switch {
	case strings.Contains(slug, "client-go"), strings.HasSuffix(slug, "/geth"):
		return "geth"
	case strings.Contains(slug, "nethermind"):
		return "nethermind"
	case strings.Contains(slug, "besu"):
		return "besu"
	case strings.Contains(slug, "reth"):
		return "reth"
	case strings.Contains(slug, "lighthouse"):
		return "lighthouse"
	case strings.Contains(slug, "teku"):
		return "teku"
	case strings.Contains(slug, "prysm"):
		return "prysm"
	case strings.Contains(slug, "nimbus"):
		return "nimbus"
	case strings.Contains(slug, "lodestar"):
		return "lodestar"
	}
	// Fallback: last path component without tag
	if idx := strings.LastIndex(slug, "/"); idx >= 0 {
		return slug[idx+1:]
	}
	return slug
}

// connectedSourceIPs returns the unique source IPs of established connections
// to the given local port, excluding loopback and link-local addresses.
// Uses `ss` which is available on all modern Linux systems.
func connectedSourceIPs(ctx context.Context, port string) ([]string, error) {
	// ss -tn state established 'sport = :<port>'
	// sport = local port (what this node is serving)
	// The Peer Address column gives us the remote (source) IP.
	cmd := exec.CommandContext(ctx, "ss",
		"-tn",
		"state", "established",
		fmt.Sprintf("sport = :%s", port),
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ss failed: %w", err)
	}

	seen := map[string]bool{}
	var ips []string

	lines := strings.Split(string(out), "\n")
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// Peer address is field 4: "18.1.2.3:54321"
		peerAddr := fields[4]
		host, _, err := net.SplitHostPort(peerAddr)
		if err != nil {
			// Try without port (IPv6 format)
			host = peerAddr
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		s := ip.String()
		if !seen[s] {
			seen[s] = true
			ips = append(ips, s)
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no external connections to port %s", port)
	}
	return ips, nil
}
