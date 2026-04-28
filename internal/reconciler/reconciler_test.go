package reconciler_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/reconciler"
	"github.com/enriquemanuel/eth-node-operator/internal/ufw"
	"github.com/enriquemanuel/eth-node-operator/pkg/dockerclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/ethclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// --- Fake docker commander ---

type fakeDocker struct {
	calls   [][]string
	outputs map[string]string
	errors  map[string]error
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{outputs: make(map[string]string), errors: make(map[string]error)}
}

func (f *fakeDocker) set(out string, args ...string) {
	f.outputs[strings.Join(args, "|")] = out
}

func (f *fakeDocker) setErr(err error, args ...string) {
	f.errors[strings.Join(args, "|")] = err
}

func (f *fakeDocker) Run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, "|")
	f.calls = append(f.calls, args)
	return f.outputs[key], f.errors[key]
}

func (f *fakeDocker) called(args ...string) bool {
	key := strings.Join(args, "|")
	for _, c := range f.calls {
		if strings.Join(c, "|") == key {
			return true
		}
	}
	return false
}

// --- Fake UFW runner ---

type fakeUFW struct {
	outputs map[string]string
	errors  map[string]error
}

func newFakeUFW() *fakeUFW {
	return &fakeUFW{outputs: make(map[string]string), errors: make(map[string]error)}
}

func (f *fakeUFW) set(out string, args ...string) {
	f.outputs[strings.Join(args, " ")] = out
}

func (f *fakeUFW) Run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	return f.outputs[key], f.errors[key]
}

// --- Helpers ---

func inspectOutput(running bool, image string) string {
	state := "false"
	if running {
		state = "true"
	}
	return fmt.Sprintf("%s|%s|2024-01-01T00:00:00Z", state, image)
}

func makeReconciler(t *testing.T, docker *fakeDocker, ufwRunner *fakeUFW) *reconciler.Reconciler {
	t.Helper()
	dockerClient := dockerclient.NewWithCommander(docker)
	ethClient := ethclient.New("", "")
	ufwMgr := ufw.NewWithRunner(ufwRunner)
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	return reconciler.New(dockerClient, ethClient, ufwMgr, log)
}

// --- Tests ---

func TestReconcile_NoActionWhenInSync(t *testing.T) {
	docker := newFakeDocker()
	docker.set(
		inspectOutput(true, "ethereum/client-go:v1.14.8"),
		"inspect", "execution", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set(
		inspectOutput(true, "sigp/lighthouse:v5.3.0"),
		"inspect", "consensus", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)

	ufwRunner := newFakeUFW()
	ufwRunner.set("Status: active\n30303/tcp ALLOW", "status", "verbose")

	r := makeReconciler(t, docker, ufwRunner)

	spec := types.NodeSpec{
		Execution: types.ClientSpec{Image: "ethereum/client-go:v1.14.8"},
		Consensus: types.ClientSpec{Image: "sigp/lighthouse:v5.3.0"},
		Network: types.NetworkSpec{
			Firewall: types.FirewallSpec{
				Provider: "ufw",
				Rules: []types.FirewallRule{
					{Port: 30303, Proto: "tcp", Direction: "inbound", Action: "allow"},
				},
			},
		},
	}

	result, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Actions) != 1 { // only firewall reconciled action
		t.Errorf("expected 1 action (firewall reconciled), got %d: %v", len(result.Actions), result.Actions)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got: %v", result.Errors)
	}
}

func TestReconcile_UpdatesELWhenImageDrifted(t *testing.T) {
	docker := newFakeDocker()
	docker.set(
		inspectOutput(true, "ethereum/client-go:v1.14.8"), // old image
		"inspect", "execution", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set(
		inspectOutput(true, "sigp/lighthouse:v5.3.0"),
		"inspect", "consensus", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set("", "pull", "ethereum/client-go:v1.14.9")
	docker.set("", "stop", "execution")
	docker.set("", "start", "execution")

	ufwRunner := newFakeUFW()

	r := makeReconciler(t, docker, ufwRunner)

	spec := types.NodeSpec{
		Execution: types.ClientSpec{Image: "ethereum/client-go:v1.14.9"}, // new version
		Consensus: types.ClientSpec{Image: "sigp/lighthouse:v5.3.0"},
	}

	result, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, a := range result.Actions {
		if strings.Contains(a, "execution") && strings.Contains(a, "v1.14.9") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected EL upgrade action, got: %v", result.Actions)
	}

	if !docker.called("pull", "ethereum/client-go:v1.14.9") {
		t.Error("expected docker pull to be called")
	}
	if !docker.called("stop", "execution") {
		t.Error("expected docker stop to be called")
	}
	if !docker.called("start", "execution") {
		t.Error("expected docker start to be called")
	}
}

func TestReconcile_StartsStoppedContainer(t *testing.T) {
	docker := newFakeDocker()
	docker.set(
		inspectOutput(false, "ethereum/client-go:v1.14.8"), // stopped but right image
		"inspect", "execution", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set(
		inspectOutput(true, "sigp/lighthouse:v5.3.0"),
		"inspect", "consensus", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set("", "start", "execution")

	ufwRunner := newFakeUFW()
	r := makeReconciler(t, docker, ufwRunner)

	spec := types.NodeSpec{
		Execution: types.ClientSpec{Image: "ethereum/client-go:v1.14.8"},
		Consensus: types.ClientSpec{Image: "sigp/lighthouse:v5.3.0"},
	}

	result, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, a := range result.Actions {
		if strings.Contains(a, "execution") && strings.Contains(a, "started") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected start action, got: %v", result.Actions)
	}

	if !docker.called("start", "execution") {
		t.Error("expected docker start to be called")
	}
}

func TestDiff_DetectsImageDrift(t *testing.T) {
	docker := newFakeDocker()
	docker.set(
		inspectOutput(true, "ethereum/client-go:v1.14.8"),
		"inspect", "execution", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set(
		inspectOutput(true, "sigp/lighthouse:v5.3.0"),
		"inspect", "consensus", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)

	ufwRunner := newFakeUFW()
	r := makeReconciler(t, docker, ufwRunner)

	spec := types.NodeSpec{
		Execution: types.ClientSpec{Image: "ethereum/client-go:v1.14.9"}, // drifted
		Consensus: types.ClientSpec{Image: "sigp/lighthouse:v5.3.0"},
	}

	diff, err := r.Diff(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if diff.InSync {
		t.Error("expected out of sync")
	}
	if len(diff.Drifts) != 1 {
		t.Errorf("expected 1 drift, got %d: %v", len(diff.Drifts), diff.Drifts)
	}
	if diff.Drifts[0].Field != "execution.image" {
		t.Errorf("unexpected drift field: %s", diff.Drifts[0].Field)
	}
}

func TestDiff_InSyncWhenMatches(t *testing.T) {
	docker := newFakeDocker()
	docker.set(
		inspectOutput(true, "ethereum/client-go:v1.14.8"),
		"inspect", "execution", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set(
		inspectOutput(true, "sigp/lighthouse:v5.3.0"),
		"inspect", "consensus", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)

	ufwRunner := newFakeUFW()
	r := makeReconciler(t, docker, ufwRunner)

	spec := types.NodeSpec{
		Execution: types.ClientSpec{Image: "ethereum/client-go:v1.14.8"},
		Consensus: types.ClientSpec{Image: "sigp/lighthouse:v5.3.0"},
	}

	diff, err := r.Diff(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !diff.InSync {
		t.Errorf("expected in sync, got drifts: %v", diff.Drifts)
	}
}

func TestReconcile_UpdatesMEV(t *testing.T) {
	docker := newFakeDocker()
	docker.set(
		inspectOutput(true, "ethereum/client-go:v1.14.8"),
		"inspect", "execution", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set(
		inspectOutput(true, "sigp/lighthouse:v5.3.0"),
		"inspect", "consensus", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set(
		inspectOutput(true, "flashbots/mev-boost:v1.8.0"), // old
		"inspect", "mev-boost", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	docker.set("", "pull", "flashbots/mev-boost:v1.8.1")
	docker.set("", "stop", "mev-boost")
	docker.set("", "start", "mev-boost")

	ufwRunner := newFakeUFW()
	r := makeReconciler(t, docker, ufwRunner)

	spec := types.NodeSpec{
		Execution: types.ClientSpec{Image: "ethereum/client-go:v1.14.8"},
		Consensus: types.ClientSpec{Image: "sigp/lighthouse:v5.3.0"},
		MEV: types.MEVSpec{
			Enabled: true,
			Image:   "flashbots/mev-boost:v1.8.1",
		},
	}

	result, err := r.Reconcile(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, a := range result.Actions {
		if strings.Contains(a, "mev-boost") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected MEV upgrade action, got: %v", result.Actions)
	}
}
