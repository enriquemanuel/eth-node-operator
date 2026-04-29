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
