// Package obs reconciles the on-host observability docker-compose stack.
// The agent owns this — no Ansible needed.
//
// It manages: node-exporter, cAdvisor, smartctl_exporter, process-exporter,
// blackbox_exporter, ipmi_exporter, ethereum-metrics-exporter, Alloy, Traefik.
package obs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner abstracts exec for testing.
type Runner interface {
	Run(ctx context.Context, dir string, name string, args ...string) (string, error)
}

type realRunner struct{}

func (r *realRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Manager reconciles the observability docker-compose stack.
type Manager struct {
	runner  Runner
	stackDir string
	envFile  string
}

// New returns a Manager for the given stack directory.
func New(stackDir, envFile string) *Manager {
	if stackDir == "" {
		stackDir = "/opt/eth-observability"
	}
	if envFile == "" {
		envFile = filepath.Join(stackDir, ".env")
	}
	return &Manager{
		runner:   &realRunner{},
		stackDir: stackDir,
		envFile:  envFile,
	}
}

// NewWithRunner returns a Manager using a custom Runner (for testing).
func NewWithRunner(r Runner, stackDir, envFile string) *Manager {
	return &Manager{runner: r, stackDir: stackDir, envFile: envFile}
}

const composeFile = "docker-compose.observability.yml"

// Reconcile ensures the observability stack is up-to-date and running.
// Steps:
//  1. Check stack directory exists
//  2. Check .env file exists (required for credentials)
//  3. Pull latest images for any services with updated tags
//  4. docker compose up -d (no-op if already current)
//
// Returns actions taken and any errors.
func (m *Manager) Reconcile(ctx context.Context) ([]string, error) {
	var actions []string

	// Check stack directory
	if _, err := os.Stat(m.stackDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("observability stack dir %s does not exist — run ethctl obs install first", m.stackDir)
	}

	// Check .env exists
	if _, err := os.Stat(m.envFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("observability .env not found at %s — copy .env.example and fill in credentials", m.envFile)
	}

	// Pull updated images
	if out, err := m.runner.Run(ctx, m.stackDir, "docker", "compose",
		"-f", composeFile, "pull", "--quiet"); err != nil {
		// Non-fatal — might be offline; use cached images
		actions = append(actions, fmt.Sprintf("obs: image pull warning: %s", out))
	} else {
		if strings.TrimSpace(out) != "" {
			actions = append(actions, "obs: pulled updated images")
		}
	}

	// Bring the stack up (idempotent)
	out, err := m.runner.Run(ctx, m.stackDir, "docker", "compose",
		"-f", composeFile, "up", "-d", "--remove-orphans", "--quiet-pull")
	if err != nil {
		return actions, fmt.Errorf("obs stack up: %s: %w", out, err)
	}

	// Check if anything changed
	if strings.Contains(out, "Started") || strings.Contains(out, "Recreated") || strings.Contains(out, "Created") {
		actions = append(actions, fmt.Sprintf("obs: stack reconciled: %s", strings.TrimSpace(out)))
	}

	return actions, nil
}

// Status returns the running state of all observability containers.
func (m *Manager) Status(ctx context.Context) (map[string]string, error) {
	out, err := m.runner.Run(ctx, m.stackDir, "docker", "compose",
		"-f", composeFile, "ps", "--format", "{{.Name}}\t{{.Status}}")
	if err != nil {
		return nil, fmt.Errorf("obs status: %w", err)
	}

	status := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			status[parts[0]] = parts[1]
		}
	}
	return status, nil
}

// IsRunning returns true if the observability stack has running containers.
func (m *Manager) IsRunning(ctx context.Context) (bool, error) {
	status, err := m.Status(ctx)
	if err != nil {
		return false, err
	}
	for _, s := range status {
		if strings.Contains(strings.ToLower(s), "up") || strings.Contains(strings.ToLower(s), "running") {
			return true, nil
		}
	}
	return false, nil
}

// Install copies the observability stack files to the stack directory.
// This is called once from ethctl or the agent bootstrap phase.
func (m *Manager) Install(ctx context.Context, sourceDir string) error {
	if err := os.MkdirAll(m.stackDir, 0750); err != nil {
		return fmt.Errorf("create stack dir: %w", err)
	}

	// Recursively copy stack files
	if _, err := m.runner.Run(ctx, "", "cp", "-r", sourceDir+"/.", m.stackDir+"/"); err != nil {
		return fmt.Errorf("copy stack files: %w", err)
	}

	// Create acme dir for Traefik certificates (must be 700)
	acmeDir := filepath.Join(m.stackDir, "traefik", "acme")
	if err := os.MkdirAll(acmeDir, 0700); err != nil {
		return fmt.Errorf("create acme dir: %w", err)
	}

	// Create acme.json (must be 600 for Traefik)
	acmeJSON := filepath.Join(acmeDir, "acme.json")
	if _, err := os.Stat(acmeJSON); os.IsNotExist(err) {
		f, err := os.OpenFile(acmeJSON, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("create acme.json: %w", err)
		}
		f.Close()
	}

	return nil
}

// StackDir returns the directory this manager uses.
func (m *Manager) StackDir() string { return m.stackDir }

// EnvFile returns the .env file path.
func (m *Manager) EnvFile() string { return m.envFile }
