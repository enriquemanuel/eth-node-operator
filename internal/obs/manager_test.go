package obs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/obs"
)

type fakeRunner struct {
	calls   []string
	outputs map[string]string
	errors  map[string]error
}

func newFake() *fakeRunner {
	return &fakeRunner{
		outputs: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (f *fakeRunner) set(out, dir, name string, args ...string) {
	key := dir + "|" + name + " " + strings.Join(args, " ")
	f.outputs[key] = out
}

func (f *fakeRunner) setErr(err error, dir, name string, args ...string) {
	key := dir + "|" + name + " " + strings.Join(args, " ")
	f.errors[key] = err
}

func (f *fakeRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	key := dir + "|" + name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, key)
	return f.outputs[key], f.errors[key]
}

func (f *fakeRunner) calledWith(name string, args ...string) bool {
	sub := name + " " + strings.Join(args, " ")
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func tempStackDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Create required files
	os.WriteFile(filepath.Join(dir, "docker-compose.observability.yml"), []byte("version: '3'"), 0644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("NODE_NAME=test\n"), 0644)
	return dir
}

func TestReconcile_StackUpToDate(t *testing.T) {
	dir := tempStackDir(t)
	fake := newFake()
	fake.set("", dir, "docker", "compose", "-f", "docker-compose.observability.yml", "pull", "--quiet")
	fake.set("", dir, "docker", "compose", "-f", "docker-compose.observability.yml", "up", "-d", "--remove-orphans", "--quiet-pull")

	m := obs.NewWithRunner(fake, dir, filepath.Join(dir, ".env"))
	actions, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.calledWith("docker", "compose", "-f", "docker-compose.observability.yml", "up") {
		t.Error("expected docker compose up to be called")
	}
	_ = actions
}

func TestReconcile_PullsAndStarts(t *testing.T) {
	dir := tempStackDir(t)
	fake := newFake()
	fake.set("Pulled alloy", dir, "docker", "compose", "-f", "docker-compose.observability.yml", "pull", "--quiet")
	fake.set("Started alloy\nStarted node-exporter", dir, "docker", "compose", "-f", "docker-compose.observability.yml", "up", "-d", "--remove-orphans", "--quiet-pull")

	m := obs.NewWithRunner(fake, dir, filepath.Join(dir, ".env"))
	actions, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should report updated images
	hasStackAction := false
	for _, a := range actions {
		if strings.Contains(a, "stack reconciled") {
			hasStackAction = true
		}
	}
	if !hasStackAction {
		t.Errorf("expected stack reconciled action, got: %v", actions)
	}
}

func TestReconcile_MissingStackDir(t *testing.T) {
	fake := newFake()
	m := obs.NewWithRunner(fake, "/does/not/exist", "/does/not/exist/.env")
	_, err := m.Reconcile(context.Background())
	if err == nil {
		t.Error("expected error for missing stack dir")
	}
}

func TestReconcile_MissingEnvFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker-compose.observability.yml"), []byte(""), 0644)
	// No .env file

	fake := newFake()
	m := obs.NewWithRunner(fake, dir, filepath.Join(dir, ".env"))
	_, err := m.Reconcile(context.Background())
	if err == nil {
		t.Error("expected error for missing .env")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("error should mention .env, got: %v", err)
	}
}

func TestReconcile_ComposeUpFailure(t *testing.T) {
	dir := tempStackDir(t)
	fake := newFake()
	fake.set("", dir, "docker", "compose", "-f", "docker-compose.observability.yml", "pull", "--quiet")
	fake.setErr(fmt.Errorf("docker daemon not running"), dir, "docker", "compose", "-f", "docker-compose.observability.yml", "up", "-d", "--remove-orphans", "--quiet-pull")

	m := obs.NewWithRunner(fake, dir, filepath.Join(dir, ".env"))
	_, err := m.Reconcile(context.Background())
	if err == nil {
		t.Error("expected error when compose up fails")
	}
}

func TestStatus_ParsesOutput(t *testing.T) {
	dir := tempStackDir(t)
	fake := newFake()
	fake.set(
		"alloy\tUp 2 hours\nnode-exporter\tUp 2 hours\ncadvisor\tUp 2 hours",
		dir, "docker", "compose", "-f", "docker-compose.observability.yml", "ps", "--format", "{{.Name}}\t{{.Status}}",
	)

	m := obs.NewWithRunner(fake, dir, filepath.Join(dir, ".env"))
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(status) != 3 {
		t.Errorf("expected 3 containers, got %d", len(status))
	}
	if status["alloy"] != "Up 2 hours" {
		t.Errorf("expected alloy Up 2 hours, got %q", status["alloy"])
	}
}

func TestIsRunning_WhenUp(t *testing.T) {
	dir := tempStackDir(t)
	fake := newFake()
	fake.set("alloy\tUp 2 hours", dir, "docker", "compose", "-f", "docker-compose.observability.yml", "ps", "--format", "{{.Name}}\t{{.Status}}")

	m := obs.NewWithRunner(fake, dir, filepath.Join(dir, ".env"))
	running, err := m.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !running {
		t.Error("expected running=true")
	}
}

func TestIsRunning_WhenDown(t *testing.T) {
	dir := tempStackDir(t)
	fake := newFake()
	fake.set("alloy\tExited (1)", dir, "docker", "compose", "-f", "docker-compose.observability.yml", "ps", "--format", "{{.Name}}\t{{.Status}}")

	m := obs.NewWithRunner(fake, dir, filepath.Join(dir, ".env"))
	running, err := m.IsRunning(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if running {
		t.Error("expected running=false when container exited")
	}
}
