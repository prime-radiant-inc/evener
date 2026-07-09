package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
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

// TestConfigureSandboxNonOffCarriesInert: with the M5 flag-live flip, a non-off
// mode NO LONGER errors at the flag boundary (the M1 feature gate is gone) — it
// parses the mode + network decision into the carrier fields so the resolved policy
// round-trips into the persisted meta. Enforcement is engaged separately by
// provisionSandbox at env construction; configureSandbox stays a pure parse.
func TestConfigureSandboxNonOffCarriesInert(t *testing.T) {
	for _, mode := range []string{"read-only", "workspace-write", "restricted"} {
		cfg := agent.SessionConfig{}
		if err := configureSandbox(&cfg, mode, "off"); err != nil {
			t.Fatalf("mode %q must no longer error at the flag boundary (gate removed): %v", mode, err)
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

// TestSandboxFlagOffIsInert proves the live-flip is a byte-identical no-op for off:
// provisionSandbox leaves the env unsandboxed (nil policy, nil wrapper) and there is
// no enforcement line. It uses the PRODUCTION provisionSandbox, which short-circuits
// off BEFORE probing the host, so the default path never forks the capability probes.
func TestSandboxFlagOffIsInert(t *testing.T) {
	worktree := t.TempDir()
	cfg := agent.SessionConfig{}
	if err := configureSandbox(&cfg, "off", "on"); err != nil {
		t.Fatalf("configureSandbox(off): %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(worktree)
	t.Cleanup(env.Cleanup)
	if err := provisionSandbox(env, &cfg, worktree); err != nil {
		t.Fatalf("off provisioning must not error: %v", err)
	}
	if env.Sandbox != nil || env.Wrapper != nil {
		t.Errorf("off must leave the env unsandboxed, got Sandbox=%v Wrapper=%v", env.Sandbox, env.Wrapper)
	}
	if line := sandboxEnforcementLine(env); line != "" {
		t.Errorf("off must have no enforcement line, got %q", line)
	}
}

// TestSandboxFlagRestrictedEnforces is the M5 acceptance proof that the live flag
// path actually enforces: --sandbox restricted, set purely through the CLI helpers
// (configureSandbox + provisionSandbox), builds an ENFORCED env on this bwrap host,
// denies an out-of-worktree write (file-tool layer), masks a credential from a
// spawned process (kernel layer), and reports a truthful enforcement line. Gated on
// a real bwrap host and skipped under -short (the integration-gate convention).
func TestSandboxFlagRestrictedEnforces(t *testing.T) {
	if testing.Short() {
		t.Skip("real-bwrap sandbox flag path skipped under -short")
	}
	facts := sandbox.RealProber{}.Probe()
	if facts.OS != "linux" || !facts.BwrapCapable || facts.BwrapPath == "" {
		t.Skip("bwrap not capable on this host")
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "SANDBOX-FLAG-SECRET-material-do-not-leak"
	secretPath := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	facts.Home = home // anchor the credential denylist at the fake home

	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	// The flag boundary: a non-off mode now carries inert without erroring (gate gone).
	cfg := agent.SessionConfig{}
	if err := configureSandbox(&cfg, "restricted", "on"); err != nil {
		t.Fatalf("configureSandbox(restricted): %v", err)
	}

	// The live-flip: provisioning builds an enforced env (file-tool layer + wrapper).
	env := execenv.NewLocalExecutionEnvironment(worktree)
	t.Cleanup(env.Cleanup)
	if err := provisionSandboxWithHost(env, &cfg, worktree, facts); err != nil {
		t.Fatalf("provisionSandboxWithHost(restricted): %v", err)
	}
	if env.Sandbox == nil || !env.Sandbox.Enforced() {
		t.Fatal("restricted flag path must build an enforced env")
	}
	if env.Wrapper == nil {
		t.Error("restricted flag path must attach a kernel wrapper")
	}

	// The startup enforcement line names the actual backend + mode (never overstates).
	line := sandboxEnforcementLine(env)
	if !strings.Contains(line, "bwrap") || !strings.Contains(line, "restricted") {
		t.Errorf("enforcement line must name bwrap + restricted, got %q", line)
	}

	// File-tool enforcement: an out-of-worktree write is a typed denial and never
	// reaches the host.
	outside := filepath.Join(home, "escape.txt")
	if _, werr := env.WriteFile(outside, "pwned"); !errors.As(werr, new(*sandbox.DeniedError)) {
		t.Errorf("out-of-worktree write under restricted must be a *sandbox.DeniedError, got %v", werr)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("the denied out-of-worktree write must not reach the host filesystem")
	}

	// Kernel enforcement: a spawned process cannot read the masked credential.
	res, err := env.ExecCommand(context.Background(), "cat "+secretPath+" 2>&1 || true", 15000, worktree, nil)
	if err != nil {
		t.Fatalf("spawn under restricted failed: %v", err)
	}
	if strings.Contains(res.Stdout+res.Stderr, secret) {
		t.Errorf("a spawned process read the masked credential through the flag-provisioned sandbox:\n%s%s", res.Stdout, res.Stderr)
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
