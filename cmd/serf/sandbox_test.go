package main

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/sandbox"
)

// TestConfigureSandboxOffIsNoop: --sandbox off (the default) leaves the config's
// carrier fields untouched, so a default session is byte-identical to today.
func TestConfigureSandboxOffIsNoop(t *testing.T) {
	cfg := agent.SessionConfig{}
	if err := configureSandbox(&cfg, "off", "on"); err != nil {
		t.Fatalf("off must not error: %v", err)
	}
	if cfg.Sandbox != "" || cfg.SandboxNet != nil {
		t.Errorf("off must leave the carrier fields zero (byte-identical no-op), got Sandbox=%q SandboxNet=%v", cfg.Sandbox, cfg.SandboxNet)
	}

	// An unset mode ("" — internal callers that skip the flag layer) is also off,
	// not an "unknown mode" error.
	empty := agent.SessionConfig{}
	if err := configureSandbox(&empty, "", ""); err != nil {
		t.Fatalf("empty mode must default to off, got error: %v", err)
	}
	if empty.Sandbox != "" || empty.SandboxNet != nil {
		t.Errorf("empty mode must leave carrier fields zero, got Sandbox=%q SandboxNet=%v", empty.Sandbox, empty.SandboxNet)
	}
}

// TestConfigureSandboxNonOffGated: every non-off mode fails session start at the
// M1 feature gate — a distinct flag-boundary error, NOT the fail-closed
// *sandbox.RefusalError (that path is exercised directly in the sandbox package's
// Resolve tests). The flags still parse INTO the config before the gate fires.
func TestConfigureSandboxNonOffGated(t *testing.T) {
	for _, mode := range []string{"read-only", "workspace-write", "restricted"} {
		cfg := agent.SessionConfig{}
		err := configureSandbox(&cfg, mode, "off")
		if err == nil {
			t.Fatalf("mode %q must fail session start (feature gate)", mode)
		}
		if !strings.Contains(err.Error(), "in development and not yet enabled") {
			t.Errorf("mode %q: want feature-gate error, got %v", mode, err)
		}
		var ref *sandbox.RefusalError
		if errors.As(err, &ref) {
			t.Errorf("mode %q: feature gate must be distinct from the fail-closed RefusalError", mode)
		}
		if cfg.Sandbox != mode {
			t.Errorf("mode %q must parse into cfg.Sandbox, got %q", mode, cfg.Sandbox)
		}
		if cfg.SandboxNet == nil || *cfg.SandboxNet {
			t.Errorf("mode %q: --sandbox-net off must parse into cfg.SandboxNet=false, got %v", mode, cfg.SandboxNet)
		}
	}
}

// TestConfigureSandboxBadValues: an unknown mode or net value is a clear error.
func TestConfigureSandboxBadValues(t *testing.T) {
	cfg := agent.SessionConfig{}
	if err := configureSandbox(&cfg, "bogus", "on"); err == nil {
		t.Error("an unknown --sandbox mode must error")
	}
	if err := configureSandbox(&cfg, "off", "maybe"); err == nil {
		t.Error("an invalid --sandbox-net value must error")
	}
}

// TestParseSandboxNet: on/off/empty map correctly (case- and space-insensitive);
// anything else errors.
func TestParseSandboxNet(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		err  bool
	}{
		{"on", true, false},
		{"off", false, false},
		{"", true, false},
		{"ON", true, false},
		{" off ", false, false},
		{"yes", false, true},
	}
	for _, tc := range cases {
		got, err := parseSandboxNet(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("parseSandboxNet(%q) should error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseSandboxNet(%q) = %v, %v; want %v, nil", tc.in, got, err, tc.want)
		}
	}
}
