package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/enriquemanuel/eth-node-operator/internal/disk"
	"github.com/enriquemanuel/eth-node-operator/internal/dns"
	"github.com/enriquemanuel/eth-node-operator/internal/snapshot"
	"github.com/enriquemanuel/eth-node-operator/internal/obs"
	"github.com/enriquemanuel/eth-node-operator/internal/ufw"
	"github.com/enriquemanuel/eth-node-operator/pkg/dockerclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/ethclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// Reconciler compares desired NodeSpec against actual state and acts on drift.
type Reconciler struct {
	docker   *dockerclient.Client
	eth      *ethclient.Client
	firewall *ufw.Manager
	diskMgr  *disk.Manager
	obsMgr   *obs.Manager
	log      *slog.Logger
}

// New returns a Reconciler with all its dependencies.
func New(docker *dockerclient.Client, eth *ethclient.Client, firewall *ufw.Manager, log *slog.Logger) *Reconciler {
	return &Reconciler{
		docker:   docker,
		eth:      eth,
		firewall: firewall,
		diskMgr:  disk.New(),
		log:      log,
	}
}

// NewWithObs returns a Reconciler that also manages the observability stack.
func NewWithObs(docker *dockerclient.Client, eth *ethclient.Client, firewall *ufw.Manager, obsMgr *obs.Manager, log *slog.Logger) *Reconciler {
	r := New(docker, eth, firewall, log)
	r.obsMgr = obsMgr
	return r
}

// Reconcile runs a full reconciliation pass in order:
//  1. Disk provisioning (RAID, format, mount, fstab)
//  2. Firewall rules
//  3. Execution client
//  4. Consensus client
//  5. MEV-Boost
//  6. Observability stack
func (r *Reconciler) Reconcile(ctx context.Context, desired types.NodeSpec) (*types.ReconcileResult, error) {
	start := time.Now()
	result := &types.ReconcileResult{}

	// 1. Disk
	diskActions, err := r.diskMgr.Reconcile(ctx, desired.System.Disk)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("disk: %v", err))
	}
	result.Actions = append(result.Actions, diskActions...)

	// 2. Snapshot restore (if datadir empty — runs before starting clients)
	if desired.Snapshot.Enabled {
		snapActions, err := r.reconcileSnapshot(ctx, desired)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("snapshot: %v", err))
		}
		result.Actions = append(result.Actions, snapActions...)
	}

	// 3. Firewall
	if desired.Network.Firewall.Provider == "ufw" {
		if err := r.reconcileFirewall(ctx, desired.Network.Firewall); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("firewall: %v", err))
		} else {
			result.Actions = append(result.Actions, "firewall: reconciled")
		}
	}


	// 4. Route 53 DNS registration (derives hostname from node name + EL client)
	if desired.Network.Route53.ZoneID != "" && dns.IsConfigured() {
		hostname := dns.Hostname(result.NodeName, desired.Execution.Client, desired.Network.Route53.Zone)
		dnsMgr := dns.New()
		dnsResult, err := dnsMgr.Register(ctx, hostname, desired.Network.Route53.ZoneID, desired.Network.Route53.TTL)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("dns: %v", err))
		} else if dnsResult.Action == "UPSERT" {
			result.Actions = append(result.Actions, fmt.Sprintf("dns: %s → %s", dnsResult.Hostname, dnsResult.IP))
		}
	}

	// 4b. VC gateway :5052 firewall rules (EKS NAT IPs → beacon API)
	if len(desired.Network.VCGateways) > 0 {
		for _, cidr := range desired.Network.VCGateways {
			rule := types.FirewallRule{
				Description: "VC beacon API",
				Port:        5052,
				Proto:       "tcp",
				Direction:   "inbound",
				Source:      cidr,
				Action:      "allow",
			}
			desired.Network.Firewall.Rules = append(desired.Network.Firewall.Rules, rule)
		}
	}

	// 5. Execution client
	if action, err := r.reconcileClient(ctx, "execution", desired.Execution); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("el: %v", err))
	} else if action != "" {
		result.Actions = append(result.Actions, action)
	}

	// 6. Consensus client
	if action, err := r.reconcileClient(ctx, "consensus", desired.Consensus); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("cl: %v", err))
	} else if action != "" {
		result.Actions = append(result.Actions, action)
	}

	// 7. MEV boost
	if desired.MEV.Enabled {
		if action, err := r.reconcileMEV(ctx, desired.MEV); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("mev: %v", err))
		} else if action != "" {
			result.Actions = append(result.Actions, action)
		}
	}

	// 8. Observability stack
	if r.obsMgr != nil && desired.Observability.Metrics.Enabled {
		obsActions, err := r.obsMgr.Reconcile(ctx)
		if err != nil {
			// Non-fatal — log it but don't fail the whole reconcile
			r.log.Warn("observability reconcile error", "err", err)
			result.Errors = append(result.Errors, fmt.Sprintf("obs: %v", err))
		} else {
			result.Actions = append(result.Actions, obsActions...)
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (r *Reconciler) reconcileSnapshot(ctx context.Context, desired types.NodeSpec) ([]string, error) {
	var actions []string

	// Pick the right snapshot base URL
	var d *snapshot.Downloader
	if desired.Snapshot.Provider == "self-hosted" && desired.Snapshot.URL != "" {
		d = snapshot.NewWithBase(desired.Snapshot.URL)
	} else {
		d = snapshot.New()
	}

	network := desired.Snapshot.Network
	if network == "" {
		network = "mainnet"
	}

	if desired.Snapshot.ELEnabled && desired.Execution.DataDir != "" {
		restored, err := d.RestoreIfEmpty(ctx, network, desired.Execution.Client, desired.Execution.DataDir)
		if err != nil {
			return actions, fmt.Errorf("EL snapshot: %w", err)
		}
		if restored {
			actions = append(actions, fmt.Sprintf("snapshot: restored %s/%s to %s",
				network, desired.Execution.Client, desired.Execution.DataDir))
		}
	}

	if desired.Snapshot.CLEnabled && desired.Consensus.DataDir != "" {
		restored, err := d.RestoreIfEmpty(ctx, network, desired.Consensus.Client, desired.Consensus.DataDir)
		if err != nil {
			return actions, fmt.Errorf("CL snapshot: %w", err)
		}
		if restored {
			actions = append(actions, fmt.Sprintf("snapshot: restored %s/%s to %s",
				network, desired.Consensus.Client, desired.Consensus.DataDir))
		}
	}

	return actions, nil
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
		r.log.Info("image drift detected", "container", containerName, "current", info.Image, "desired", desired.Image)
		if err := r.docker.Pull(ctx, desired.Image); err != nil {
			return "", fmt.Errorf("pull %s: %w", desired.Image, err)
		}
		if err := r.docker.Stop(ctx, containerName); err != nil {
			return "", fmt.Errorf("stop %s: %w", containerName, err)
		}
		if err := r.docker.Start(ctx, containerName); err != nil {
			return "", fmt.Errorf("start %s: %w", containerName, err)
		}
		return fmt.Sprintf("%s: updated %s → %s", containerName, info.Image, desired.Image), nil
	}

	if !info.Running {
		r.log.Info("container stopped, starting", "container", containerName)
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

// Diff compares desired spec against actual state.
func (r *Reconciler) Diff(ctx context.Context, desired types.NodeSpec) (*types.DiffResult, error) {
	result := &types.DiffResult{InSync: true}

	if desired.Execution.Image != "" {
		info, err := r.docker.Inspect(ctx, "execution")
		if err == nil && info.Image != desired.Execution.Image {
			result.InSync = false
			result.Drifts = append(result.Drifts, types.DriftItem{
				Field: "execution.image", Desired: desired.Execution.Image, Actual: info.Image,
			})
		}
		if err == nil && !info.Running {
			result.InSync = false
			result.Drifts = append(result.Drifts, types.DriftItem{
				Field: "execution.running", Desired: "true", Actual: "false",
			})
		}
	}

	if desired.Consensus.Image != "" {
		info, err := r.docker.Inspect(ctx, "consensus")
		if err == nil && info.Image != desired.Consensus.Image {
			result.InSync = false
			result.Drifts = append(result.Drifts, types.DriftItem{
				Field: "consensus.image", Desired: desired.Consensus.Image, Actual: info.Image,
			})
		}
	}

	if desired.MEV.Enabled && desired.MEV.Image != "" {
		info, err := r.docker.Inspect(ctx, "mev-boost")
		if err == nil && info.Image != desired.MEV.Image {
			result.InSync = false
			result.Drifts = append(result.Drifts, types.DriftItem{
				Field: "mev.image", Desired: desired.MEV.Image, Actual: info.Image,
			})
		}
	}

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

// PreflightChecks validates node readiness before upgrade.
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
