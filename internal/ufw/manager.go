package ufw

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// Runner abstracts command execution for testing.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type realRunner struct{}

func (r *realRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "ufw", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Manager applies and reads UFW firewall rules.
type Manager struct {
	runner Runner
}

// New returns a Manager using real ufw commands.
func New() *Manager {
	return &Manager{runner: &realRunner{}}
}

// NewWithRunner returns a Manager using a custom Runner (for testing).
func NewWithRunner(r Runner) *Manager {
	return &Manager{runner: r}
}

// Status returns the raw output of ufw status verbose.
func (m *Manager) Status(ctx context.Context) (string, error) {
	out, err := m.runner.Run(ctx, "status", "verbose")
	if err != nil {
		return "", fmt.Errorf("ufw status: %w", err)
	}
	return out, nil
}

// IsEnabled returns true if UFW is active.
func (m *Manager) IsEnabled(ctx context.Context) (bool, error) {
	out, err := m.runner.Run(ctx, "status")
	if err != nil {
		return false, fmt.Errorf("ufw status: %w", err)
	}
	lower := strings.ToLower(out)
	return strings.Contains(lower, "status: active") && !strings.Contains(lower, "inactive"), nil
}

// Enable enables UFW if it is not already active.
func (m *Manager) Enable(ctx context.Context) error {
	enabled, err := m.IsEnabled(ctx)
	if err != nil {
		return err
	}
	if enabled {
		return nil
	}
	_, err = m.runner.Run(ctx, "--force", "enable")
	if err != nil {
		return fmt.Errorf("ufw enable: %w", err)
	}
	return nil
}

// SetDefaultPolicy sets the default incoming policy (allow or deny).
func (m *Manager) SetDefaultPolicy(ctx context.Context, policy string) error {
	action := "deny"
	if policy == "allow-by-default" {
		action = "allow"
	}
	_, err := m.runner.Run(ctx, "default", action, "incoming")
	if err != nil {
		return fmt.Errorf("ufw default %s: %w", action, err)
	}
	_, err = m.runner.Run(ctx, "default", "allow", "outgoing")
	return err
}

// ApplyRules reconciles the desired firewall rules. It resets UFW and reapplies all rules.
func (m *Manager) ApplyRules(ctx context.Context, spec types.FirewallSpec) error {
	if err := m.Enable(ctx); err != nil {
		return err
	}

	if err := m.SetDefaultPolicy(ctx, spec.Policy); err != nil {
		return err
	}

	// Reset all rules before reapplying to ensure idempotency.
	if _, err := m.runner.Run(ctx, "--force", "reset"); err != nil {
		return fmt.Errorf("ufw reset: %w", err)
	}

	for _, rule := range spec.Rules {
		if err := m.applyRule(ctx, rule); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) applyRule(ctx context.Context, rule types.FirewallRule) error {
	args := buildRuleArgs(rule)
	_, err := m.runner.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("apply ufw rule %+v: %w", rule, err)
	}
	return nil
}

// buildRuleArgs converts a FirewallRule to ufw command arguments.
func buildRuleArgs(rule types.FirewallRule) []string {
	var args []string

	switch strings.ToLower(rule.Direction) {
	case "inbound":
		if rule.Source != "" {
			args = append(args, "allow", "from", rule.Source, "to", "any", "port",
				fmt.Sprintf("%d", rule.Port), "proto", rule.Proto)
		} else {
			args = append(args, rule.Action, fmt.Sprintf("%d/%s", rule.Port, rule.Proto))
		}
	case "outbound":
		if rule.Destination != "" {
			args = append(args, rule.Action, "out", "to", rule.Destination, "port",
				fmt.Sprintf("%d", rule.Port), "proto", rule.Proto)
		} else {
			args = append(args, rule.Action, "out", fmt.Sprintf("%d/%s", rule.Port, rule.Proto))
		}
	default:
		args = append(args, rule.Action, fmt.Sprintf("%d/%s", rule.Port, rule.Proto))
	}

	return args
}

// DeleteRule removes a specific UFW rule.
func (m *Manager) DeleteRule(ctx context.Context, rule types.FirewallRule) error {
	args := append([]string{"delete"}, buildRuleArgs(rule)...)
	_, err := m.runner.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("delete ufw rule: %w", err)
	}
	return nil
}

// ListRules returns the numbered list of active UFW rules.
func (m *Manager) ListRules(ctx context.Context) ([]string, error) {
	out, err := m.runner.Run(ctx, "status", "numbered")
	if err != nil {
		return nil, fmt.Errorf("ufw status numbered: %w", err)
	}

	var rules []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			rules = append(rules, line)
		}
	}
	return rules, nil
}

// DriftCheck compares desired rules against active rules.
// Returns a list of rules that are in the desired state but not applied.
func (m *Manager) DriftCheck(ctx context.Context, spec types.FirewallSpec) ([]types.FirewallRule, error) {
	activeRaw, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}

	var missing []types.FirewallRule
	for _, rule := range spec.Rules {
		portStr := fmt.Sprintf("%d", rule.Port)
		if !strings.Contains(activeRaw, portStr) {
			missing = append(missing, rule)
		}
	}
	return missing, nil
}
