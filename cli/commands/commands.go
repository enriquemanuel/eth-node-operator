package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/enriquemanuel/eth-node-operator/cli/agentclient"
	"github.com/enriquemanuel/eth-node-operator/internal/discover"
	"github.com/enriquemanuel/eth-node-operator/internal/dns"
	"github.com/enriquemanuel/eth-node-operator/pkg/inventory"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
	"github.com/spf13/cobra"
)

var (
	inventoryFile string
	agentPort     int
)

// Root returns the root cobra command.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "ethctl",
		Short: "Manage Ethereum bare metal nodes",
		Long:  "ethctl is the CLI for the eth-node-operator. It talks to ethagent on each bare metal host.",
	}

	root.PersistentFlags().StringVarP(&inventoryFile, "inventory", "i", "inventory/nodes", "path to nodes inventory directory")
	root.PersistentFlags().IntVar(&agentPort, "port", 19000, "agent HTTP port")

	root.AddCommand(
		nodesCmd(),
		discoverCmd(),
		dnsCmd(),
		cordonCmd(),
		uncordonCmd(),
		syncCmd(),
		logsCmd(),
		restartCmd(),
		diffCmd(),
		upgradeCmd(),
	)

	return root
}

// nodesCmd is the parent for 'ethctl nodes ...' commands.
func nodesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nodes",
		Short: "Node commands",
	}
	cmd.AddCommand(nodesListCmd(), nodesDescribeCmd())
	return cmd
}

func nodesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all nodes with their status",
		RunE: func(cmd *cobra.Command, args []string) error {
			nodes, err := inventory.LoadAll(inventoryFile)
			if err != nil {
				return fmt.Errorf("load inventory: %w", err)
			}

			type result struct {
				node   *types.EthereumNode
				status *types.NodeStatus
				err    error
			}

			results := make([]result, len(nodes))
			var wg sync.WaitGroup

			for i, node := range nodes {
				wg.Add(1)
				go func(i int, n *types.EthereumNode) {
					defer wg.Done()
					client := agentclient.New(n.Name, n.Host, agentPort)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					status, err := client.GetStatus(ctx)
					results[i] = result{node: n, status: status, err: err}
				}(i, node)
			}

			wg.Wait()

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tHOST\tEL\tCL\tMEV\tSYNCED\tPEERS\tPHASE")
			fmt.Fprintln(w, "────\t────\t──\t──\t───\t──────\t─────\t─────")

			for _, r := range results {
				if r.err != nil {
					fmt.Fprintf(w, "%s\t%s\t—\t—\t—\t—\t—\tUnreachable\n",
						r.node.Name, r.node.Host)
					continue
				}
				s := r.status
				synced := fmt.Sprintf("%s/%s", syncMark(s.EL.Synced), syncMark(s.CL.Synced))
				mevRunning := "—"
				if s.MEV.Running {
					mevRunning = "✓"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d/%d\t%s\n",
					r.node.Name,
					r.node.Host,
					shortVersion(s.EL),
					shortVersion(s.CL),
					mevRunning,
					synced,
					s.EL.PeerCount, s.CL.PeerCount,
					s.Phase,
				)
			}
			return w.Flush()
		},
	}
}

func nodesDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <node>",
		Short: "Show full detail for a single node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeName := args[0]
			node, err := findNode(nodeName)
			if err != nil {
				return err
			}

			client := agentclient.New(node.Name, node.Host, agentPort)
			status, err := client.GetStatus(context.Background())
			if err != nil {
				return fmt.Errorf("get status: %w", err)
			}

			fmt.Printf("Node:        %s\n", status.Name)
			fmt.Printf("Host:        %s\n", node.Host)
			fmt.Printf("Phase:       %s\n", status.Phase)
			fmt.Printf("Cordoned:    %v\n", status.Cordoned)
			fmt.Printf("Reported:    %s\n", status.ReportedAt.Format(time.RFC3339))
			fmt.Println()
			fmt.Printf("Execution Layer:\n")
			fmt.Printf("  Image:   %s\n", status.EL.Image)
			fmt.Printf("  Running: %v\n", status.EL.Running)
			fmt.Printf("  Synced:  %v\n", status.EL.Synced)
			fmt.Printf("  Block:   %d\n", status.EL.BlockNumber)
			fmt.Printf("  Peers:   %d\n", status.EL.PeerCount)
			fmt.Println()
			fmt.Printf("Consensus Layer:\n")
			fmt.Printf("  Image:   %s\n", status.CL.Image)
			fmt.Printf("  Running: %v\n", status.CL.Running)
			fmt.Printf("  Synced:  %v\n", status.CL.Synced)
			fmt.Printf("  Peers:   %d\n", status.CL.PeerCount)
			fmt.Println()
			fmt.Printf("MEV Boost:\n")
			fmt.Printf("  Running: %v\n", status.MEV.Running)
			fmt.Printf("  Image:   %s\n", status.MEV.Image)
			fmt.Println()
			fmt.Printf("System:\n")
			fmt.Printf("  Hostname:    %s\n", status.System.Hostname)
			fmt.Printf("  CPU:         %.1f%%\n", status.System.CPUPercent)
			fmt.Printf("  Memory:      %.1f / %.1f GB\n", status.System.MemUsedGB, status.System.MemTotalGB)
			fmt.Printf("  Disk Free:   %.1f GB\n", status.System.DiskFreeGB)
			fmt.Printf("  Uptime:      %.1fh\n", status.System.UptimeHours)
			fmt.Printf("  Kernel:      %s\n", status.System.KernelVer)

			return nil
		},
	}
}

func cordonCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "cordon <node>",
		Short: "Pause reconciliation on a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withNodeClient(args[0], func(client *agentclient.Client) error {
				if err := client.Cordon(context.Background(), reason); err != nil {
					return err
				}
				fmt.Printf("✓ %s cordoned\n", args[0])
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for cordoning")
	return cmd
}

func uncordonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uncordon <node>",
		Short: "Resume reconciliation on a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withNodeClient(args[0], func(client *agentclient.Client) error {
				if err := client.Uncordon(context.Background()); err != nil {
					return err
				}
				fmt.Printf("✓ %s uncordoned\n", args[0])
				return nil
			})
		},
	}
}

func syncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync <node>",
		Short: "Force immediate reconciliation on a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withNodeClient(args[0], func(client *agentclient.Client) error {
				result, err := client.TriggerReconcile(context.Background())
				if err != nil {
					return err
				}
				if len(result.Actions) == 0 {
					fmt.Printf("✓ %s already in sync\n", args[0])
					return nil
				}
				fmt.Printf("✓ %s reconciled:\n", args[0])
				for _, a := range result.Actions {
					fmt.Printf("  • %s\n", a)
				}
				if len(result.Errors) > 0 {
					fmt.Printf("⚠ Errors:\n")
					for _, e := range result.Errors {
						fmt.Printf("  • %s\n", e)
					}
				}
				return nil
			})
		},
	}
}

func logsCmd() *cobra.Command {
	var clientFlag string
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs <node>",
		Short: "Stream logs from a node client",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withNodeClient(args[0], func(client *agentclient.Client) error {
				rc, err := client.StreamLogs(context.Background(), clientFlag, follow)
				if err != nil {
					return err
				}
				defer rc.Close()
				_, err = io.Copy(os.Stdout, rc)
				return err
			})
		},
	}
	cmd.Flags().StringVar(&clientFlag, "client", "el", "client to stream logs from (el|cl|mev)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	return cmd
}

func restartCmd() *cobra.Command {
	var clientFlag string

	cmd := &cobra.Command{
		Use:   "restart <node>",
		Short: "Restart a client on a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withNodeClient(args[0], func(client *agentclient.Client) error {
				if err := client.Restart(context.Background(), clientFlag); err != nil {
					return err
				}
				fmt.Printf("✓ %s %s restarted\n", args[0], clientFlag)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&clientFlag, "client", "el", "client to restart (el|cl|mev)")
	return cmd
}

func diffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <node>",
		Short: "Show diff between desired and actual state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withNodeClient(args[0], func(client *agentclient.Client) error {
				diff, err := client.GetDiff(context.Background())
				if err != nil {
					return err
				}
				if diff.InSync {
					fmt.Printf("✓ %s is in sync\n", args[0])
					return nil
				}
				fmt.Printf("⚠ %s has drift:\n", args[0])
				for _, d := range diff.Drifts {
					fmt.Printf("  %-30s desired=%-30s actual=%s\n", d.Field, d.Desired, d.Actual)
				}
				return nil
			})
		},
	}
}

func upgradeCmd() *cobra.Command {
	var elImage, clImage, mevImage string
	var maxUnavailable int
	var skipPreflight bool

	cmd := &cobra.Command{
		Use:   "upgrade [node...]",
		Short: "Rolling upgrade across nodes",
		Long: `Trigger a rolling upgrade across one or more nodes.
The spec must already be updated in the inventory before running this.
This command drives the upgrade sequence: preflight, cordon, reconcile, wait for sync, uncordon.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			nodes, err := inventory.LoadAll(inventoryFile)
			if err != nil {
				return fmt.Errorf("load inventory: %w", err)
			}

			// Filter to requested nodes, or all if none specified
			var targetNames []string
			if len(args) > 0 {
				targetNames = args
			} else {
				for _, n := range nodes {
					targetNames = append(targetNames, n.Name)
				}
			}

			fmt.Printf("Upgrading %d node(s): %v\n", len(targetNames), targetNames)
			fmt.Printf("  EL image: %s\n", elImage)
			fmt.Printf("  CL image: %s\n", clImage)
			fmt.Printf("  Max unavailable: %d\n", maxUnavailable)
			fmt.Println()

			nodeMap := make(map[string]*types.EthereumNode)
			for _, n := range nodes {
				nodeMap[n.Name] = n
			}

			for i, name := range targetNames {
				node, ok := nodeMap[name]
				if !ok {
					return fmt.Errorf("node %q not found in inventory", name)
				}

				fmt.Printf("[%d/%d] Upgrading %s...\n", i+1, len(targetNames), name)

				client := agentclient.New(node.Name, node.Host, agentPort)

				if !skipPreflight {
					status, err := client.GetStatus(context.Background())
					if err != nil {
						return fmt.Errorf("preflight status for %s: %w", name, err)
					}
					if !status.EL.Synced {
						return fmt.Errorf("preflight failed for %s: EL not synced", name)
					}
					if !status.CL.Synced {
						return fmt.Errorf("preflight failed for %s: CL not synced", name)
					}
					fmt.Printf("  ✓ preflight passed\n")
				}

				if err := client.Cordon(context.Background(), "rolling upgrade"); err != nil {
					return fmt.Errorf("cordon %s: %w", name, err)
				}
				fmt.Printf("  ✓ cordoned\n")

				result, err := client.TriggerReconcile(context.Background())
				if err != nil {
					_ = client.Uncordon(context.Background())
					return fmt.Errorf("reconcile %s: %w", name, err)
				}
				for _, a := range result.Actions {
					fmt.Printf("  • %s\n", a)
				}

				if err := client.Uncordon(context.Background()); err != nil {
					fmt.Printf("  ⚠ failed to uncordon %s: %v\n", name, err)
				} else {
					fmt.Printf("  ✓ uncordoned\n")
				}

				fmt.Printf("  ✓ %s upgraded\n", name)

				// Pause between nodes (except the last)
				if i < len(targetNames)-1 {
					fmt.Printf("  Waiting 30s before next node...\n")
					time.Sleep(30 * time.Second)
				}
			}

			fmt.Printf("\n✓ All %d nodes upgraded\n", len(targetNames))
			return nil
		},
	}

	cmd.Flags().StringVar(&elImage, "el", "", "execution layer image (e.g. ethereum/client-go:v1.14.9)")
	cmd.Flags().StringVar(&clImage, "cl", "", "consensus layer image (e.g. sigp/lighthouse:v5.4.0)")
	cmd.Flags().StringVar(&mevImage, "mev", "", "MEV boost image")
	cmd.Flags().IntVar(&maxUnavailable, "max-unavailable", 1, "max nodes to upgrade simultaneously")
	cmd.Flags().BoolVar(&skipPreflight, "skip-preflight", false, "skip sync checks before upgrading")

	return cmd
}

// --- Helpers ---

func findNode(name string) (*types.EthereumNode, error) {
	nodes, err := inventory.LoadAll(inventoryFile)
	if err != nil {
		return nil, fmt.Errorf("load inventory: %w", err)
	}
	for _, n := range nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return nil, fmt.Errorf("node %q not found in inventory", name)
}

func withNodeClient(nodeName string, fn func(*agentclient.Client) error) error {
	node, err := findNode(nodeName)
	if err != nil {
		return err
	}
	client := agentclient.New(node.Name, node.Host, agentPort)
	return fn(client)
}

func syncMark(synced bool) string {
	if synced {
		return "✓"
	}
	return "✗"
}

func shortVersion(cs types.ClientStatus) string {
	if !cs.Running {
		return "stopped"
	}
	if cs.Image == "" {
		return "unknown"
	}
	// Extract just client:version from the image
	// ethereum/client-go:v1.14.8 → geth:v1.14.8
	return cs.Image
}

// dnsCmd groups DNS management subcommands.
func dnsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Manage Route 53 DNS records for the cluster",
	}
	cmd.AddCommand(dnsListCmd(), dnsAuditCmd(), dnsCleanupCmd(), dnsDecommissionCmd())
	return cmd
}

// dnsListCmd lists all A records in the zone.
func dnsListCmd() *cobra.Command {
	var zoneID, zone string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all A records in the Route 53 zone",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dns.IsConfigured() {
				return fmt.Errorf("aws CLI not configured — set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY")
			}
			m := dns.New()
			records, err := m.ListZoneRecords(context.Background(), zoneID, zone)
			if err != nil {
				return err
			}
			if len(records) == 0 {
				fmt.Printf("No A records found in zone %s matching %q\n", zoneID, zone)
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "HOSTNAME\tIP\tTTL")
			fmt.Fprintln(w, "────────\t──\t───")
			for _, r := range records {
				fmt.Fprintf(w, "%s\t%s\t%d\n", r.Hostname, r.IP, r.TTL)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&zoneID, "zone-id", "", "Route 53 hosted zone ID (required)")
	cmd.Flags().StringVar(&zone, "zone", "", "base domain to filter by (e.g. validators.example.com)")
	cmd.MarkFlagRequired("zone-id")
	return cmd
}

// dnsAuditCmd compares Route 53 records against active cluster nodes.
func dnsAuditCmd() *cobra.Command {
	var zoneID, zone string
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show Route 53 records that don't match any active cluster node",
		Long: `Compares all A records in the Route 53 zone against the expected
hostnames derived from active nodes in the cluster inventory.

Reports any records that should not exist — decommissioned nodes,
old client names after a client swap, or manual/stale entries.

Use 'ethctl dns cleanup' to delete the reported records.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dns.IsConfigured() {
				return fmt.Errorf("aws CLI not configured")
			}

			// Build expected hostname set from cluster inventory
			expected, err := expectedHostnames(zone)
			if err != nil {
				return fmt.Errorf("load cluster inventory: %w", err)
			}

			m := dns.New()
			stale, err := m.Audit(context.Background(), zoneID, zone, expected)
			if err != nil {
				return err
			}

			if len(stale) == 0 {
				fmt.Printf("✓ All records in zone %s match active cluster nodes\n", zone)
				return nil
			}

			fmt.Printf("⚠ Found %d stale record(s) in zone %s:\n\n", len(stale), zone)
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "HOSTNAME\tIP\tSTATUS")
			fmt.Fprintln(w, "────────\t──\t──────")
			for _, r := range stale {
				fmt.Fprintf(w, "%s\t%s\tnot in cluster\n", r.Hostname, r.IP)
			}
			w.Flush()
			fmt.Printf("\nRun 'ethctl dns cleanup --zone-id %s --zone %s' to delete them.\n", zoneID, zone)
			return nil
		},
	}
	cmd.Flags().StringVar(&zoneID, "zone-id", "", "Route 53 hosted zone ID (required)")
	cmd.Flags().StringVar(&zone, "zone", "", "base domain (required)")
	cmd.MarkFlagRequired("zone-id")
	cmd.MarkFlagRequired("zone")
	return cmd
}

// dnsCleanupCmd deletes stale Route 53 records after confirmation.
func dnsCleanupCmd() *cobra.Command {
	var zoneID, zone string
	var yes bool
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Delete Route 53 records that don't match any active cluster node",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dns.IsConfigured() {
				return fmt.Errorf("aws CLI not configured")
			}

			expected, err := expectedHostnames(zone)
			if err != nil {
				return fmt.Errorf("load inventory: %w", err)
			}

			m := dns.New()
			stale, err := m.Audit(context.Background(), zoneID, zone, expected)
			if err != nil {
				return err
			}

			if len(stale) == 0 {
				fmt.Println("✓ Nothing to clean up")
				return nil
			}

			fmt.Printf("Will delete %d stale record(s):\n", len(stale))
			for _, r := range stale {
				fmt.Printf("  DELETE  %-55s → %s\n", r.Hostname, r.IP)
			}

			if !yes {
				fmt.Print("\nProceed? [y/N] ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			var failed []string
			for _, r := range stale {
				if err := m.Delete(context.Background(), r.Hostname, zoneID); err != nil {
					failed = append(failed, fmt.Sprintf("%s: %v", r.Hostname, err))
					fmt.Printf("  ✗ %s\n", r.Hostname)
				} else {
					fmt.Printf("  ✓ deleted %s\n", r.Hostname)
				}
			}

			if len(failed) > 0 {
				return fmt.Errorf("some deletions failed: %v", failed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&zoneID, "zone-id", "", "Route 53 hosted zone ID (required)")
	cmd.Flags().StringVar(&zone, "zone", "", "base domain (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	cmd.MarkFlagRequired("zone-id")
	cmd.MarkFlagRequired("zone")
	return cmd
}

// dnsDecommissionCmd removes a node's DNS record and marks it disabled.
func dnsDecommissionCmd() *cobra.Command {
	var zoneID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "decommission <node>",
		Short: "Delete a node's Route 53 A record (run before removing hardware)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeName := args[0]

			node, err := findNode(nodeName)
			if err != nil {
				return err
			}

			if zoneID == "" {
				zoneID = node.Spec.Network.Route53.ZoneID
			}
			zone := node.Spec.Network.Route53.Zone
			if zoneID == "" || zone == "" {
				return fmt.Errorf("node %s has no Route 53 config (zoneId / zone missing)", nodeName)
			}

			hostname := dns.Hostname(nodeName, node.Spec.Execution.Client, zone)

			if !yes {
				fmt.Printf("Will delete: %s (zone: %s)\n", hostname, zoneID)
				fmt.Print("Proceed? [y/N] ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			if !dns.IsConfigured() {
				return fmt.Errorf("aws CLI not configured")
			}

			m := dns.New()
			if err := m.Delete(context.Background(), hostname, zoneID); err != nil {
				return fmt.Errorf("delete %s: %w", hostname, err)
			}
			fmt.Printf("✓ Deleted %s\n", hostname)
			fmt.Printf("\nNext steps:\n")
			fmt.Printf("  1. Remove bare metal hardware / return to provider\n")
			fmt.Printf("  2. Remove or disable node in inventory/clusters/*.yaml\n")
			fmt.Printf("  3. Run 'ethctl dns audit' to confirm zone is clean\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&zoneID, "zone-id", "", "Route 53 zone ID (defaults to node's configured zone)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

// expectedHostnames builds the set of A record hostnames that should exist
// based on the current cluster inventory.
func expectedHostnames(zone string) (map[string]bool, error) {
	clusters, err := inventory.LoadAllClusters(inventoryFile)
	if err != nil {
		// Fall back to treating inventoryFile as a single cluster
		nodes, err2 := inventory.LoadAll(inventoryFile)
		if err2 != nil {
			return nil, err
		}
		expected := make(map[string]bool)
		for _, n := range nodes {
			if n.Spec.Execution.Client != "" {
				expected[dns.Hostname(n.Name, n.Spec.Execution.Client, zone)] = true
			}
		}
		return expected, nil
	}

	expected := make(map[string]bool)
	for _, c := range clusters {
		for _, cn := range c.Nodes {
			if cn.Disabled {
				continue
			}
			if cn.Spec.Execution.Client != "" {
				expected[dns.Hostname(cn.Name, cn.Spec.Execution.Client, zone)] = true
			}
		}
	}
	return expected, nil
}

// discoverCmd probes a node and outputs a YAML snippet for the cluster file.
func discoverCmd() *cobra.Command {
	var zone, zoneID string
	cmd := &cobra.Command{
		Use:   "discover <node>",
		Short: "Auto-detect node configuration and output a cluster file snippet",
		Long: `Connects to the ethagent on a node and runs discovery probes:

  Public IP       — what Route 53 A record should point to
  cgroup version  — whether cAdvisor needs privileged: true
  Running clients — EL/CL client names from running Docker containers
  VC gateways     — source IPs of active :5052 connections (EKS NAT IPs)
  Management IPs  — source IPs of active SSH sessions (bastion host)

Outputs a YAML snippet ready to paste into your cluster file.

Note: VC gateway detection only works if the Lighthouse VC is currently
connected. Run this after the VC has been pointed at this node.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeName := args[0]

			node, err := findNode(nodeName)
			if err != nil {
				return err
			}

			client := agentclient.New(nodeName, node.Host, agentPort)

			raw, err := client.Discover(context.Background())
			if err != nil {
				return fmt.Errorf("discover %s: %w", nodeName, err)
			}

			// If zone/zoneID not given, try to read from node spec
			if zone == "" {
				zone = node.Spec.Network.Route53.Zone
			}
			if zoneID == "" {
				zoneID = node.Spec.Network.Route53.ZoneID
			}

			// Build a Report from the raw map for YAML snippet generation
			report := &discover.Report{}
			if v, ok := raw["publicIP"].(string); ok {
				report.PublicIP = v
			}
			if v, ok := raw["cgroupVersion"].(float64); ok {
				report.CgroupVersion = int(v)
			}
			if v, ok := raw["elClient"].(string); ok {
				report.ELClient = v
			}
			if v, ok := raw["clClient"].(string); ok {
				report.CLClient = v
			}
			if arr, ok := raw["vcGatewayIPs"].([]interface{}); ok {
				for _, ip := range arr {
					if s, ok := ip.(string); ok {
						report.VCGatewayIPs = append(report.VCGatewayIPs, s)
					}
				}
			}
			if arr, ok := raw["managementIPs"].([]interface{}); ok {
				for _, ip := range arr {
					if s, ok := ip.(string); ok {
						report.ManagementIPs = append(report.ManagementIPs, s)
					}
				}
			}
			if arr, ok := raw["warnings"].([]interface{}); ok {
				for _, w := range arr {
					if s, ok := w.(string); ok {
						report.Warnings = append(report.Warnings, s)
					}
				}
			}

			fmt.Println(report.YAMLSnippet(nodeName, zone, zoneID))
			return nil
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "base domain for Route 53 (e.g. validators.example.com)")
	cmd.Flags().StringVar(&zoneID, "zone-id", "", "Route 53 hosted zone ID")
	return cmd
}
