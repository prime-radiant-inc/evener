package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

func boolPtr(v bool) *bool { return &v }

// TestBuildDelegateSandboxPolicy_ModeFloor: a delegate may only request a box at
// least as confining as its parent's. Under a restricted parent every looser mode
// is refused with a legible invalid_request error; under an off parent every mode
// is allowed (off is loosest).
func TestBuildDelegateSandboxPolicy_ModeFloor(t *testing.T) {
	t.Parallel()

	// Restricted parent (net on): only restricted is allowed; off/read-only/
	// workspace-write all grant more access and must be refused.
	for _, mode := range []string{"off", "read-only", "workspace-write"} {
		pol, err := buildDelegateSandboxPolicy(mode, nil, sandbox.ModeRestricted, true)
		if err == nil {
			t.Errorf("delegate %q under a restricted parent must be refused, got policy %+v", mode, pol)
			continue
		}
		if !strings.Contains(err.Error(), "invalid_request:") {
			t.Errorf("delegate %q refusal must be an invalid_request error, got %v", mode, err)
		}
		if !strings.Contains(err.Error(), "grants more access than your own sandbox") {
			t.Errorf("delegate %q refusal must explain the escalation, got %v", mode, err)
		}
	}
	if _, err := buildDelegateSandboxPolicy("restricted", nil, sandbox.ModeRestricted, true); err != nil {
		t.Errorf("restricted delegate under a restricted parent must be allowed, got %v", err)
	}

	// Off parent: every mode is allowed and the returned policy carries the
	// requested mode.
	for name, mode := range map[string]sandbox.Mode{
		"off":             sandbox.ModeOff,
		"read-only":       sandbox.ModeReadOnly,
		"workspace-write": sandbox.ModeWorkspaceWrite,
		"restricted":      sandbox.ModeRestricted,
	} {
		pol, err := buildDelegateSandboxPolicy(name, nil, sandbox.ModeOff, true)
		if err != nil {
			t.Errorf("delegate %q under an off parent must be allowed, got %v", name, err)
			continue
		}
		if pol == nil || pol.Mode != mode {
			t.Errorf("delegate %q under an off parent = %+v, want mode %v", name, pol, mode)
		}
	}
}

// TestBuildDelegateSandboxPolicy_NetworkFloor: a delegate may turn the network OFF
// (tighter) but never ON. An omitted net inherits the parent's effective net; an
// explicit net-on under a net-off parent is refused.
func TestBuildDelegateSandboxPolicy_NetworkFloor(t *testing.T) {
	t.Parallel()

	// Net omitted under a net-off parent inherits off (stays tight).
	pol, err := buildDelegateSandboxPolicy("restricted", nil, sandbox.ModeRestricted, false)
	if err != nil {
		t.Fatalf("restricted delegate under a net-off restricted parent must be allowed: %v", err)
	}
	if pol.Network == nil || *pol.Network {
		t.Errorf("omitted net under a net-off parent must inherit off, got %+v", pol.Network)
	}

	// Explicit net-on under a net-off parent is refused.
	if _, err := buildDelegateSandboxPolicy("restricted", boolPtr(true), sandbox.ModeRestricted, false); err == nil {
		t.Error("explicit sandbox_net=on under a net-off parent must be refused")
	} else if !strings.Contains(err.Error(), "invalid_request:") {
		t.Errorf("network escalation refusal must be an invalid_request error, got %v", err)
	}

	// Explicit net-off under a net-on parent is allowed (tightening).
	pol, err = buildDelegateSandboxPolicy("restricted", boolPtr(false), sandbox.ModeRestricted, true)
	if err != nil {
		t.Fatalf("explicit net-off under a net-on parent must be allowed: %v", err)
	}
	if pol.Network == nil || *pol.Network {
		t.Errorf("explicit net-off must yield Network=false, got %+v", pol.Network)
	}

	// Net omitted under a net-on parent inherits on.
	pol, err = buildDelegateSandboxPolicy("restricted", nil, sandbox.ModeOff, true)
	if err != nil {
		t.Fatalf("restricted delegate under an off (net-on) parent must be allowed: %v", err)
	}
	if pol.Network == nil || !*pol.Network {
		t.Errorf("omitted net under a net-on parent must inherit on, got %+v", pol.Network)
	}
}

// TestBuildDelegateSandboxPolicy_UnknownMode: a mistyped mode fails loudly as an
// invalid_request rather than silently disabling the box.
func TestBuildDelegateSandboxPolicy_UnknownMode(t *testing.T) {
	t.Parallel()
	if _, err := buildDelegateSandboxPolicy("bogus", nil, sandbox.ModeOff, true); err == nil {
		t.Fatal("an unknown sandbox mode must be refused")
	} else if !strings.Contains(err.Error(), "invalid_request:") {
		t.Errorf("unknown mode refusal must be an invalid_request error, got %v", err)
	}
}
