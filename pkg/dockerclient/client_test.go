package dockerclient_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/pkg/dockerclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// FakeCommander records calls and returns preconfigured outputs.
type FakeCommander struct {
	calls   [][]string
	outputs map[string]string
	errors  map[string]error
}

func newFake() *FakeCommander {
	return &FakeCommander{
		outputs: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (f *FakeCommander) Run(_ context.Context, args ...string) (string, error) {
	key := fmt.Sprintf("%v", args)
	f.calls = append(f.calls, args)
	return f.outputs[key], f.errors[key]
}

func (f *FakeCommander) SetOutput(args []string, out string) {
	f.outputs[fmt.Sprintf("%v", args)] = out
}

func (f *FakeCommander) SetError(args []string, err error) {
	f.errors[fmt.Sprintf("%v", args)] = err
}

func TestInspect_RunningContainer(t *testing.T) {
	fake := newFake()
	fake.SetOutput(
		[]string{"inspect", "execution", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}"},
		"true|ethereum/client-go:v1.14.8|2024-01-01T00:00:00Z",
	)

	c := dockerclient.NewWithCommander(fake)
	info, err := c.Inspect(context.Background(), "execution")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.Running {
		t.Error("expected container to be running")
	}
	if info.Image != "ethereum/client-go:v1.14.8" {
		t.Errorf("unexpected image: %s", info.Image)
	}
	if info.Version != "v1.14.8" {
		t.Errorf("unexpected version: %s", info.Version)
	}
}

func TestInspect_StoppedContainer(t *testing.T) {
	fake := newFake()
	fake.SetOutput(
		[]string{"inspect", "consensus", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}"},
		"false|sigp/lighthouse:v5.3.0|2024-01-01T00:00:00Z",
	)

	c := dockerclient.NewWithCommander(fake)
	info, err := c.Inspect(context.Background(), "consensus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Running {
		t.Error("expected container to be stopped")
	}
}

func TestInspect_MissingContainer(t *testing.T) {
	fake := newFake()
	fake.SetError(
		[]string{"inspect", "missing", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}"},
		fmt.Errorf("no such container"),
	)

	c := dockerclient.NewWithCommander(fake)
	_, err := c.Inspect(context.Background(), "missing")
	if err == nil {
		t.Error("expected error for missing container")
	}
}

func TestStop(t *testing.T) {
	fake := newFake()
	fake.SetOutput([]string{"stop", "execution"}, "execution")

	c := dockerclient.NewWithCommander(fake)
	if err := c.Stop(context.Background(), "execution"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0][0] != "stop" {
		t.Errorf("expected stop command, got %v", fake.calls)
	}
}

func TestStart(t *testing.T) {
	fake := newFake()
	fake.SetOutput([]string{"start", "consensus"}, "consensus")

	c := dockerclient.NewWithCommander(fake)
	if err := c.Start(context.Background(), "consensus"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRestart(t *testing.T) {
	fake := newFake()
	fake.SetOutput([]string{"restart", "execution"}, "execution")

	c := dockerclient.NewWithCommander(fake)
	if err := c.Restart(context.Background(), "execution"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListRunning(t *testing.T) {
	fake := newFake()
	fake.SetOutput([]string{"ps", "--format", "{{.Names}}"}, "execution\nconsensus\nmev-boost")

	c := dockerclient.NewWithCommander(fake)
	names, err := c.ListRunning(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 3 {
		t.Errorf("expected 3 containers, got %d: %v", len(names), names)
	}
}

func TestListRunning_Empty(t *testing.T) {
	fake := newFake()
	fake.SetOutput([]string{"ps", "--format", "{{.Names}}"}, "")

	c := dockerclient.NewWithCommander(fake)
	names, err := c.ListRunning(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected 0 containers, got %d", len(names))
	}
}

func TestContainerNames(t *testing.T) {
	spec := types.NodeSpec{
		Execution: types.ClientSpec{Client: "geth"},
		Consensus: types.ClientSpec{Client: "lighthouse"},
	}
	names := dockerclient.ContainerNames(spec)
	if names["el"] != "execution" {
		t.Errorf("expected el=execution, got %s", names["el"])
	}
	if names["cl"] != "consensus" {
		t.Errorf("expected cl=consensus, got %s", names["cl"])
	}
	if names["mev"] != "mev-boost" {
		t.Errorf("expected mev=mev-boost, got %s", names["mev"])
	}
}

func TestInspectAll_PartialFailure(t *testing.T) {
	fake := newFake()
	fake.SetOutput(
		[]string{"inspect", "execution", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}"},
		"true|ethereum/client-go:v1.14.8|2024-01-01T00:00:00Z",
	)
	fake.SetError(
		[]string{"inspect", "consensus", "--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}"},
		fmt.Errorf("not found"),
	)

	c := dockerclient.NewWithCommander(fake)
	result := c.InspectAll(context.Background(), []string{"execution", "consensus"})

	if !result["execution"].Running {
		t.Error("expected execution to be running")
	}
	if result["consensus"].Running {
		t.Error("expected consensus to be not running on error")
	}
}
