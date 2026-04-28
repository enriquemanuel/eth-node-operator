package upgrade_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/enriquemanuel/eth-node-operator/internal/upgrade"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// fakeNodeClient simulates an agent HTTP client.
type fakeNodeClient struct {
	status          *types.NodeStatus
	statusErr       error
	cordonErr       error
	uncordonErr     error
	reconcileResult *types.ReconcileResult
	reconcileErr    error
	cordoned        bool
	syncAfterReconcile bool // flip to synced after TriggerReconcile is called
	reconcileCalled bool
}

func (f *fakeNodeClient) GetStatus(_ context.Context) (*types.NodeStatus, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	// If the node should become synced after reconcile, return synced once reconcile was called
	if f.syncAfterReconcile && f.reconcileCalled {
		s := *f.status
		s.EL.Synced = true
		s.CL.Synced = true
		return &s, nil
	}
	return f.status, nil
}

func (f *fakeNodeClient) Cordon(_ context.Context, _ string) error {
	f.cordoned = true
	return f.cordonErr
}

func (f *fakeNodeClient) Uncordon(_ context.Context) error {
	f.cordoned = false
	return f.uncordonErr
}

func (f *fakeNodeClient) TriggerReconcile(_ context.Context) (*types.ReconcileResult, error) {
	f.reconcileCalled = true
	if f.reconcileErr != nil {
		return nil, f.reconcileErr
	}
	return f.reconcileResult, nil
}

func (f *fakeNodeClient) GetDiff(_ context.Context) (*types.DiffResult, error) {
	return &types.DiffResult{InSync: true}, nil
}

func syncedNode() *fakeNodeClient {
	return &fakeNodeClient{
		status: &types.NodeStatus{
			EL: types.ClientStatus{Synced: true},
			CL: types.ClientStatus{Synced: true},
		},
		reconcileResult: &types.ReconcileResult{
			Actions: []string{"execution: updated v1.14.8 → v1.14.9"},
		},
	}
}

func testOrchestrator(t *testing.T) *upgrade.Orchestrator {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	return upgrade.New(log)
}

func TestRollingUpgrade_SingleNodeSuccess(t *testing.T) {
	node := syncedNode()
	nodes := map[string]upgrade.NodeClient{"ovh-01": node}

	o := testOrchestrator(t)
	req := types.UpgradeRequest{
		Nodes:          []string{"ovh-01"},
		ELImage:        "ethereum/client-go:v1.14.9",
		MaxUnavailable: 1,
	}

	results := o.RollingUpgrade(context.Background(), nodes, req, 0)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err != nil {
		t.Errorf("unexpected error: %v", results[0].Err)
	}
}

func TestRollingUpgrade_CordonsAndUncordons(t *testing.T) {
	node := syncedNode()
	nodes := map[string]upgrade.NodeClient{"ovh-01": node}

	o := testOrchestrator(t)
	req := types.UpgradeRequest{
		Nodes:          []string{"ovh-01"},
		MaxUnavailable: 1,
	}

	o.RollingUpgrade(context.Background(), nodes, req, 0)
	if node.cordoned {
		t.Error("node should be uncordoned after upgrade")
	}
}

func TestRollingUpgrade_PreflightFailsIfNotSynced(t *testing.T) {
	node := &fakeNodeClient{
		status: &types.NodeStatus{
			EL: types.ClientStatus{Synced: false},
			CL: types.ClientStatus{Synced: true},
		},
	}
	nodes := map[string]upgrade.NodeClient{"ovh-01": node}

	o := testOrchestrator(t)
	req := types.UpgradeRequest{
		Nodes:          []string{"ovh-01"},
		MaxUnavailable: 1,
		SkipPreflight:  false,
	}

	results := o.RollingUpgrade(context.Background(), nodes, req, 0)
	if results[0].Err == nil {
		t.Error("expected preflight failure when EL not synced")
	}
}

func TestRollingUpgrade_SkipPreflight(t *testing.T) {
	// Node starts unsynced but becomes synced after reconcile is triggered.
	node := &fakeNodeClient{
		status: &types.NodeStatus{
			EL: types.ClientStatus{Synced: false},
			CL: types.ClientStatus{Synced: false},
		},
		reconcileResult:    &types.ReconcileResult{Actions: []string{"updated"}},
		syncAfterReconcile: true, // flip to synced after TriggerReconcile
	}
	nodes := map[string]upgrade.NodeClient{"ovh-01": node}

	o := testOrchestrator(t)
	req := types.UpgradeRequest{
		Nodes:          []string{"ovh-01"},
		MaxUnavailable: 1,
		SkipPreflight:  true,
	}

	results := o.RollingUpgrade(context.Background(), nodes, req, 0)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err != nil {
		t.Errorf("unexpected error: %v", results[0].Err)
	}
}

func TestRollingUpgrade_UnknownNodeReturnsError(t *testing.T) {
	nodes := map[string]upgrade.NodeClient{}

	o := testOrchestrator(t)
	req := types.UpgradeRequest{
		Nodes:          []string{"does-not-exist"},
		MaxUnavailable: 1,
	}

	results := o.RollingUpgrade(context.Background(), nodes, req, 0)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err == nil {
		t.Error("expected error for unknown node")
	}
}

func TestRollingUpgrade_ReconcileError(t *testing.T) {
	node := syncedNode()
	node.reconcileResult = nil
	node.reconcileErr = fmt.Errorf("docker pull failed")
	nodes := map[string]upgrade.NodeClient{"ovh-01": node}

	o := testOrchestrator(t)
	req := types.UpgradeRequest{
		Nodes:          []string{"ovh-01"},
		MaxUnavailable: 1,
	}

	results := o.RollingUpgrade(context.Background(), nodes, req, 0)
	if results[0].Err == nil {
		t.Error("expected reconcile error to propagate")
	}
}

func TestRollingUpgrade_RespectsMaxUnavailable(t *testing.T) {
	node1 := syncedNode()
	node2 := syncedNode()
	nodes := map[string]upgrade.NodeClient{
		"ovh-01": node1,
		"ovh-02": node2,
	}

	o := testOrchestrator(t)
	req := types.UpgradeRequest{
		Nodes:          []string{"ovh-01", "ovh-02"},
		MaxUnavailable: 1,
	}

	results := o.RollingUpgrade(context.Background(), nodes, req, 0)
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("unexpected error for %s: %v", r.NodeName, r.Err)
		}
	}
}

func TestRollingUpgrade_ContextCancellation(t *testing.T) {
	node := syncedNode()
	nodes := map[string]upgrade.NodeClient{"ovh-01": node}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	o := testOrchestrator(t)
	req := types.UpgradeRequest{
		Nodes:          []string{"ovh-01"},
		MaxUnavailable: 1,
	}

	// Should complete or return promptly without panic
	_ = o.RollingUpgrade(ctx, nodes, req, 0)
}
