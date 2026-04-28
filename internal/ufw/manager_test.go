package ufw_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/ufw"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

type fakeRunner struct {
	calls   [][]string
	outputs map[string]string
	errors  map[string]error
}

func newFake() *fakeRunner {
	return &fakeRunner{
		outputs: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, args)
	return f.outputs[key], f.errors[key]
}

func (f *fakeRunner) set(output string, args ...string) {
	f.outputs[strings.Join(args, " ")] = output
}

func (f *fakeRunner) setErr(err error, args ...string) {
	f.errors[strings.Join(args, " ")] = err
}

func (f *fakeRunner) called(args ...string) bool {
	key := strings.Join(args, " ")
	for _, c := range f.calls {
		if strings.Join(c, " ") == key {
			return true
		}
	}
	return false
}

func TestIsEnabled_Active(t *testing.T) {
	fake := newFake()
	fake.set("Status: active", "status")

	m := ufw.NewWithRunner(fake)
	enabled, err := m.IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Error("expected UFW to be active")
	}
}

func TestIsEnabled_Inactive(t *testing.T) {
	fake := newFake()
	fake.set("Status: inactive", "status")

	m := ufw.NewWithRunner(fake)
	enabled, err := m.IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Error("expected UFW to be inactive")
	}
}

func TestEnable_WhenAlreadyActive(t *testing.T) {
	fake := newFake()
	fake.set("Status: active", "status")

	m := ufw.NewWithRunner(fake)
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// should not call --force enable when already active
	if fake.called("--force", "enable") {
		t.Error("should not call ufw enable when already active")
	}
}

func TestEnable_WhenInactive(t *testing.T) {
	fake := newFake()
	fake.set("Status: inactive", "status")
	fake.set("Firewall is active and enabled", "--force", "enable")

	m := ufw.NewWithRunner(fake)
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.called("--force", "enable") {
		t.Error("expected ufw --force enable to be called")
	}
}

func TestSetDefaultPolicy_DenyByDefault(t *testing.T) {
	fake := newFake()
	fake.set("Default incoming policy changed to 'deny'", "default", "deny", "incoming")
	fake.set("Default outgoing policy changed to 'allow'", "default", "allow", "outgoing")

	m := ufw.NewWithRunner(fake)
	if err := m.SetDefaultPolicy(context.Background(), "deny-by-default"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.called("default", "deny", "incoming") {
		t.Error("expected deny incoming policy to be set")
	}
}

func TestSetDefaultPolicy_AllowByDefault(t *testing.T) {
	fake := newFake()
	fake.set("", "default", "allow", "incoming")
	fake.set("", "default", "allow", "outgoing")

	m := ufw.NewWithRunner(fake)
	if err := m.SetDefaultPolicy(context.Background(), "allow-by-default"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.called("default", "allow", "incoming") {
		t.Error("expected allow incoming policy to be set")
	}
}

func TestApplyRules_ResetsAndReapplies(t *testing.T) {
	fake := newFake()
	fake.set("Status: active", "status")
	fake.set("", "default", "deny", "incoming")
	fake.set("", "default", "allow", "outgoing")
	fake.set("Firewall reset", "--force", "reset")
	fake.set("Rule added", "allow", "30303/tcp")
	fake.set("Rule added", "allow", "9000/udp")

	spec := types.FirewallSpec{
		Provider: "ufw",
		Policy:   "deny-by-default",
		Rules: []types.FirewallRule{
			{Port: 30303, Proto: "tcp", Direction: "inbound", Action: "allow"},
			{Port: 9000, Proto: "udp", Direction: "inbound", Action: "allow"},
		},
	}

	m := ufw.NewWithRunner(fake)
	if err := m.ApplyRules(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.called("--force", "reset") {
		t.Error("expected ufw reset before reapplying rules")
	}
	if !fake.called("allow", "30303/tcp") {
		t.Error("expected p2p rule to be applied")
	}
	if !fake.called("allow", "9000/udp") {
		t.Error("expected CL p2p rule to be applied")
	}
}

func TestApplyRules_SourceRestrictedRule(t *testing.T) {
	fake := newFake()
	fake.set("Status: active", "status")
	fake.set("", "default", "deny", "incoming")
	fake.set("", "default", "allow", "outgoing")
	fake.set("", "--force", "reset")
	fake.set("Rule added", "allow", "from", "10.0.0.0/8", "to", "any", "port", "9090", "proto", "tcp")

	spec := types.FirewallSpec{
		Policy: "deny-by-default",
		Rules: []types.FirewallRule{
			{
				Port:      9090,
				Proto:     "tcp",
				Direction: "inbound",
				Source:    "10.0.0.0/8",
				Action:    "allow",
				Description: "Prometheus scrape",
			},
		},
	}

	m := ufw.NewWithRunner(fake)
	if err := m.ApplyRules(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.called("allow", "from", "10.0.0.0/8", "to", "any", "port", "9090", "proto", "tcp") {
		t.Error("expected source-restricted rule to be applied correctly")
	}
}

func TestApplyRules_OutboundRule(t *testing.T) {
	fake := newFake()
	fake.set("Status: active", "status")
	fake.set("", "default", "deny", "incoming")
	fake.set("", "default", "allow", "outgoing")
	fake.set("", "--force", "reset")
	fake.set("Rule added", "allow", "out", "to", "10.1.1.1", "port", "443", "proto", "tcp")

	spec := types.FirewallSpec{
		Policy: "deny-by-default",
		Rules: []types.FirewallRule{
			{
				Port:        443,
				Proto:       "tcp",
				Direction:   "outbound",
				Destination: "10.1.1.1",
				Action:      "allow",
			},
		},
	}

	m := ufw.NewWithRunner(fake)
	if err := m.ApplyRules(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fake.called("allow", "out", "to", "10.1.1.1", "port", "443", "proto", "tcp") {
		t.Error("expected outbound rule with destination")
	}
}

func TestDriftCheck_DetectsMissingRule(t *testing.T) {
	fake := newFake()
	// UFW status doesn't mention port 9100
	fake.set("Status: active\nTo        Action  From\n30303/tcp ALLOW   Anywhere", "status", "verbose")

	spec := types.FirewallSpec{
		Rules: []types.FirewallRule{
			{Port: 30303, Proto: "tcp", Direction: "inbound", Action: "allow"},
			{Port: 9100, Proto: "tcp", Direction: "inbound", Action: "allow"},
		},
	}

	m := ufw.NewWithRunner(fake)
	missing, err := m.DriftCheck(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(missing) != 1 {
		t.Errorf("expected 1 missing rule, got %d", len(missing))
	}
	if missing[0].Port != 9100 {
		t.Errorf("expected missing port 9100, got %d", missing[0].Port)
	}
}

func TestDriftCheck_NoMissingRules(t *testing.T) {
	fake := newFake()
	fake.set("Status: active\n30303/tcp ALLOW\n9000/udp ALLOW", "status", "verbose")

	spec := types.FirewallSpec{
		Rules: []types.FirewallRule{
			{Port: 30303, Proto: "tcp", Direction: "inbound", Action: "allow"},
			{Port: 9000, Proto: "udp", Direction: "inbound", Action: "allow"},
		},
	}

	m := ufw.NewWithRunner(fake)
	missing, err := m.DriftCheck(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(missing) != 0 {
		t.Errorf("expected no missing rules, got %d", len(missing))
	}
}

func TestStatus_Error(t *testing.T) {
	fake := newFake()
	fake.setErr(fmt.Errorf("permission denied"), "status", "verbose")

	m := ufw.NewWithRunner(fake)
	_, err := m.Status(context.Background())
	if err == nil {
		t.Error("expected error when ufw fails")
	}
}
