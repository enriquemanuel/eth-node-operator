package upgrade

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// NodeClient is the interface the orchestrator uses to interact with each agent.
type NodeClient interface {
	GetStatus(ctx context.Context) (*types.NodeStatus, error)
	Cordon(ctx context.Context, reason string) error
	Uncordon(ctx context.Context) error
	TriggerReconcile(ctx context.Context) (*types.ReconcileResult, error)
	GetDiff(ctx context.Context) (*types.DiffResult, error)
}

// Orchestrator drives rolling upgrades across a group of nodes.
type Orchestrator struct {
	log *slog.Logger
}

// New returns an Orchestrator.
func New(log *slog.Logger) *Orchestrator {
	return &Orchestrator{log: log}
}

// RollingUpgrade performs a rolling upgrade across the given nodes.
// It upgrades at most maxUnavailable nodes at a time.
// Between each node it waits pauseBetween before moving to the next.
func (o *Orchestrator) RollingUpgrade(
	ctx context.Context,
	nodes map[string]NodeClient,
	req types.UpgradeRequest,
	pauseBetween time.Duration,
) []UpgradeNodeResult {
	results := make([]UpgradeNodeResult, 0, len(req.Nodes))
	mu := sync.Mutex{}

	sem := make(chan struct{}, max(req.MaxUnavailable, 1))

	for i, nodeName := range req.Nodes {
		if i > 0 && pauseBetween > 0 {
			select {
			case <-ctx.Done():
				return results
			case <-time.After(pauseBetween):
			}
		}

		client, ok := nodes[nodeName]
		if !ok {
			mu.Lock()
			results = append(results, UpgradeNodeResult{
				NodeName: nodeName,
				Err:      fmt.Errorf("node %q not found in client map", nodeName),
			})
			mu.Unlock()
			continue
		}

		sem <- struct{}{}
		go func(name string, c NodeClient) {
			defer func() { <-sem }()
			result := o.upgradeNode(ctx, name, c, req)
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(nodeName, client)
	}

	// Wait for all goroutines to finish
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}

	return results
}

// UpgradeNodeResult captures the outcome of upgrading a single node.
type UpgradeNodeResult struct {
	NodeName string
	Actions  []string
	Err      error
	Duration time.Duration
}

func (o *Orchestrator) upgradeNode(
	ctx context.Context,
	nodeName string,
	client NodeClient,
	req types.UpgradeRequest,
) UpgradeNodeResult {
	start := time.Now()
	result := UpgradeNodeResult{NodeName: nodeName}

	// 1. Pre-flight checks
	if !req.SkipPreflight {
		status, err := client.GetStatus(ctx)
		if err != nil {
			result.Err = fmt.Errorf("get status for preflight: %w", err)
			return result
		}
		if !status.EL.Synced {
			result.Err = fmt.Errorf("preflight failed: EL not synced")
			return result
		}
		if !status.CL.Synced {
			result.Err = fmt.Errorf("preflight failed: CL not synced")
			return result
		}
	}

	// 2. Cordon the node so the reconcile loop doesn't interfere
	if err := client.Cordon(ctx, "rolling upgrade"); err != nil {
		result.Err = fmt.Errorf("cordon: %w", err)
		return result
	}
	result.Actions = append(result.Actions, "cordoned")

	defer func() {
		// Always uncordon when done
		if err := client.Uncordon(ctx); err != nil {
			o.log.Warn("failed to uncordon after upgrade", "node", nodeName, "err", err)
		} else {
			o.log.Info("uncordoned after upgrade", "node", nodeName)
		}
	}()

	// 3. Trigger reconcile with the new desired state already in the spec
	// (The spec must already be updated in git/inventory before calling this)
	reconcileResult, err := client.TriggerReconcile(ctx)
	if err != nil {
		result.Err = fmt.Errorf("trigger reconcile: %w", err)
		return result
	}
	result.Actions = append(result.Actions, reconcileResult.Actions...)

	if len(reconcileResult.Errors) > 0 {
		result.Err = fmt.Errorf("reconcile errors: %v", reconcileResult.Errors)
		return result
	}

	// 4. Wait for sync to recover after restart
	if err := o.waitForSync(ctx, client, 10*time.Minute); err != nil {
		result.Err = fmt.Errorf("wait for sync: %w", err)
		return result
	}
	result.Actions = append(result.Actions, "sync recovered")

	result.Duration = time.Since(start)
	o.log.Info("node upgraded", "node", nodeName, "duration", result.Duration)
	return result
}

// waitForSync polls until both EL and CL are synced or timeout.
// It checks immediately before starting the tick loop for fast test paths.
func (o *Orchestrator) waitForSync(ctx context.Context, client NodeClient, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	check := func() (bool, error) {
		status, err := client.GetStatus(ctx)
		if err != nil {
			return false, err
		}
		return status.EL.Synced && status.CL.Synced, nil
	}

	// Immediate check avoids 15s wait when node is already synced (common in tests)
	if synced, err := check(); err == nil && synced {
		return nil
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for sync after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			synced, err := check()
			if err != nil {
				o.log.Warn("get status during sync wait", "err", err)
				continue
			}
			if synced {
				return nil
			}
			o.log.Info("still waiting for sync")
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
