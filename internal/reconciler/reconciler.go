package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/enriquemanuel/eth-node-operator/internal/ufw"
	"github.com/enriquemanuel/eth-node-operator/pkg/dockerclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/ethclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// Reconciler compares desired NodeSpec against actual state and acts on drift.
type Reconciler struct {
	docker  *dockerclient.Client
	eth     *ethclient.Client
	firewall *ufw.Manager
	log     *slog.Logger
}

// New returns a Reconciler with all its dependencies.
func New(docker *dockerclient.Client, eth *ethclient.Client, firewall *ufw.Manager, log *slog.Logger) *Reconciler {
	return &Reconciler{
		docker:  docker,
		eth:     eth,
		firewall: firewall,
		log:     log,
	}
}

// Reconcile runs a single reconciliation pass. It returns the actions taken and any errors.
func (r *Reconciler) Reconcile(ctx context.Context, desired types.NodeSpec) (*types.ReconcileResult, error) {
	start := time.Now()
	result := &types.ReconcileResult{}

	// 1. Reconcile execution client image
	if action, err := r.reconcileClient(ctx, "execution", desired.Execution); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("el: %v", err))
	} else if action != "" {
		result.Actions = append(result.Actions, action)
	}

	// 2. Reconcile consensus client image
	if action, err := r.reconcileClient(ctx, "consensus", desired.Consensus); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("cl: %v", err))
	} else if action != "" {
		result.Actions = append(result.Actions, action)
	}

	// 3. Reconcile MEV boost
	if desired.MEV.Enabled {
		if action, err := r.reconcileMEV(ctx, desired.MEV); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("mev: %v", err))
		} else if action != "" {
			result.Actions = append(result.Actions, action)
		}
	}

	// 4. Reconcile firewall rules
	if desired.Network.Firewall.Provider == "ufw" {
		if err := r.reconcileFirewall(ctx, desired.Network.Firewall); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("firewall: %v", err))
		} else {
			result.Actions = append(result.Actions, "firewall: reconciled")
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (r *Reconciler) reconcileClient(ctx context.Context, containerName string, desired types.ClientSpec) (string, error) {
	if desired.Image == "" {
		return "", nil
	}

	info, err := r.docker.Inspect(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", containerName, err)
	}

	if info.Image != desired.Image {
		r.log.Info("image drift detected",
			"container", containerName,
			"current", info.Image,
			"desired", desired.Image,
		)

		if err := r.docker.Pull(ctx, desired.Image); err != nil {
			return "", fmt.Errorf("pull %s: %w", desired.Image, err)
		}
		if err := r.docker.Stop(ctx, containerName); err != nil {
			return "", fmt.Errorf("stop %s: %w", containerName, err)
		}
		if err := r.docker.Start(ctx, containerName); err != nil {
			return "", fmt.Errorf("start %s after update: %w", containerName, err)
		}
		return fmt.Sprintf("%s: updated %s → %s", containerName, info.Image, desired.Image), nil
	}

	if !info.Running {
		r.log.Info("container not running, starting", "container", containerName)
		if err := r.docker.Start(ctx, containerName); err != nil {
			return "", fmt.Errorf("start %s: %w", containerName, err)
		}
		return fmt.Sprintf("%s: started (was stopped)", containerName), nil
	}

	return "", nil
}

func (r *Reconciler) reconcileMEV(ctx context.Context, desired types.MEVSpec) (string, error) {
	if desired.Image == "" {
		return "", nil
	}

	info, err := r.docker.Inspect(ctx, "mev-boost")
	if err != nil {
		return "", fmt.Errorf("inspect mev-boost: %w", err)
	}

	if info.Image != desired.Image {
		if err := r.docker.Pull(ctx, desired.Image); err != nil {
			return "", err
		}
		if err := r.docker.Stop(ctx, "mev-boost"); err != nil {
			return "", err
		}
		if err := r.docker.Start(ctx, "mev-boost"); err != nil {
			return "", err
		}
		return fmt.Sprintf("mev-boost: updated %s → %s", info.Image, desired.Image), nil
	}

	if !info.Running {
		if err := r.docker.Start(ctx, "mev-boost"); err != nil {
			return "", err
		}
		return "mev-boost: started (was stopped)", nil
	}

	return "", nil
}

func (r *Reconciler) reconcileFirewall(ctx context.Context, desired types.FirewallSpec) error {
	missing, err := r.firewall.DriftCheck(ctx, desired)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}

	r.log.Info("firewall drift detected", "missingRules", len(missing))
	return r.firewall.ApplyRules(ctx, desired)
}

// Diff compares desired spec against actual state and returns drift items.
func (r *Reconciler) Diff(ctx context.Context, desired types.NodeSpec) (*types.DiffResult, error) {
	result := &types.DiffResult{InSync: true}

	// Check EL image
	if desired.Execution.Image != "" {
		info, err := r.docker.Inspect(ctx, "execution")
		if err == nil && info.Image != desired.Execution.Image {
			result.InSync = false
			result.Drifts = append(result.Drifts, types.DriftItem{
				Field:   "execution.image",
				Desired: desired.Execution.Image,
				Actual:  info.Image,
			})
		}
		if err == nil && !info.Running {
			result.InSync = false
			result.Drifts = append(result.Drifts, types.DriftItem{
				Field:   "execution.running",
				Desired: "true",
				Actual:  "false",
			})
		}
	}

	// Check CL image
	if desired.Consensus.Image != "" {
		info, err := r.docker.Inspect(ctx, "consensus")
		if err == nil && info.Image != desired.Consensus.Image {
			result.InSync = false
			result.Drifts = append(result.Drifts, types.DriftItem{
				Field:   "consensus.image",
				Desired: desired.Consensus.Image,
				Actual:  info.Image,
			})
		}
	}

	// Check MEV
	if desired.MEV.Enabled && desired.MEV.Image != "" {
		info, err := r.docker.Inspect(ctx, "mev-boost")
		if err == nil && info.Image != desired.MEV.Image {
			result.InSync = false
			result.Drifts = append(result.Drifts, types.DriftItem{
				Field:   "mev.image",
				Desired: desired.MEV.Image,
				Actual:  info.Image,
			})
		}
	}

	// Check firewall drift
	if desired.Network.Firewall.Provider == "ufw" {
		missing, err := r.firewall.DriftCheck(ctx, desired.Network.Firewall)
		if err == nil && len(missing) > 0 {
			result.InSync = false
			for _, rule := range missing {
				result.Drifts = append(result.Drifts, types.DriftItem{
					Field:   fmt.Sprintf("firewall.rule[%d/%s]", rule.Port, rule.Proto),
					Desired: "present",
					Actual:  "missing",
				})
			}
		}
	}

	return result, nil
}

// PreflightChecks validates that a node is safe to upgrade.
func (r *Reconciler) PreflightChecks(ctx context.Context, checks []string) error {
	for _, check := range checks {
		switch strings.ToLower(check) {
		case "elsynced":
			syncing, err := r.eth.ELSyncing(ctx)
			if err != nil {
				return fmt.Errorf("preflight elSynced: %w", err)
			}
			if syncing {
				return fmt.Errorf("preflight failed: EL is still syncing")
			}

		case "clsynced":
			syncing, err := r.eth.CLSyncing(ctx)
			if err != nil {
				return fmt.Errorf("preflight clSynced: %w", err)
			}
			if syncing {
				return fmt.Errorf("preflight failed: CL is still syncing")
			}

		case "peercount":
			peers, err := r.eth.ELPeerCount(ctx)
			if err != nil {
				return fmt.Errorf("preflight peerCount: %w", err)
			}
			if peers < 5 {
				return fmt.Errorf("preflight failed: only %d EL peers (minimum 5)", peers)
			}
		}
	}
	return nil
}
