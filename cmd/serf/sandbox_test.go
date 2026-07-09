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

// TestSandboxReprovisionAfterCleanup proves EnableSandbox is re-entrant after a
// Cleanup: provisioning, disposing the env, and provisioning again must mint a FRESH
// session tmp and a usable enforced env rather than leaving a stale one pointing at a
// disposed tmp. This property backs any path that re-provisions an env (e.g. a
// delegate re-rooting/resuming onto a cleaned env). Gated on a real bwrap host and
// skipped under -short.
func TestSandboxReprovisionAfterCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("real-bwrap sandbox re-provision skipped under -short")
	}
	facts := sandbox.RealProber{}.Probe()
	if facts.OS != "linux" || !facts.BwrapCapable || facts.BwrapPath == "" {
		t.Skip("bwrap not capable on this host")
	}
	home := t.TempDir()
	facts.Home = home
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := agent.SessionConfig{}
	if err := configureSandbox(&cfg, "workspace-write", "on"); err != nil {
		t.Fatalf("configureSandbox: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(worktree)
	t.Cleanup(env.Cleanup)
	if err := provisionSandboxWithHost(env, &cfg, worktree, facts); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if env.Wrapper == nil {
		t.Fatal("first provision must attach a wrapper")
	}
	tmp1 := env.Wrapper.SessionTmp()
	if _, err := os.Stat(tmp1); err != nil {
		t.Fatalf("first session tmp must exist: %v", err)
	}

	// The old session's Close() disposes the SHARED env (the /clear scenario).
	env.Cleanup()
	if _, err := os.Stat(tmp1); err == nil {
		t.Error("Cleanup must dispose the first session tmp")
	}

	// The /clear fix: re-provisioning rebuilds an enforced env with a FRESH tmp.
	if err := provisionSandboxWithHost(env, &cfg, worktree, facts); err != nil {
		t.Fatalf("re-provision after cleanup: %v", err)
	}
	if env.Sandbox == nil || !env.Sandbox.Enforced() || env.Wrapper == nil {
		t.Fatal("re-provision must rebuild an enforced env")
	}
	tmp2 := env.Wrapper.SessionTmp()
	if tmp2 == tmp1 {
		t.Error("re-provision must mint a fresh tmp, not reuse the disposed one")
	}
	if _, err := os.Stat(tmp2); err != nil {
		t.Errorf("re-provisioned session tmp must exist: %v", err)
	}
	// A spawn under the re-provisioned env runs (the cleared session is usable).
	res, err := env.ExecCommand(context.Background(), "echo REPROVISION-OK", 15000, worktree, nil)
	if err != nil {
		t.Fatalf("spawn after re-provision failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "REPROVISION-OK") {
		t.Errorf("spawn after re-provision did not run: %q %q", res.Stdout, res.Stderr)
	}
}

// TestReconcileClearSandbox: serve's /clear reuses the env, so the cleared session's
// config must inherit the env's ACTUAL sandbox (which on resume is the persisted
// mode, not the launch flag). An enforced env stamps its mode+net onto the cleared
// config; an off env clears the carrier so a launch flag can't make a cleared session
// persist a sandbox it isn't running. Hermetic: reconcile reads only the resolved
// policy inputs, so env.Sandbox is set directly (no bwrap needed).
func TestReconcileClearSandbox(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	host := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true}
	net := false
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: &net}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Enforced env (restricted, net off) but the launch flag was off: the cleared
	// config must inherit the env's mode + net so persisted and runtime agree.
	env := execenv.NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	cfg := agent.SessionConfig{}
	reconcileClearSandbox(&cfg, env)
	if cfg.Sandbox != "restricted" {
		t.Errorf("cleared config must inherit the env mode, got %q", cfg.Sandbox)
	}
	if cfg.SandboxNet == nil || *cfg.SandboxNet {
		t.Errorf("cleared config must inherit net=off from the env, got %v", cfg.SandboxNet)
	}

	// Off env: the carrier is cleared even if the launch flag set a mode, so a
	// cleared session never persists a sandbox its env isn't enforcing.
	offEnv := execenv.NewLocalExecutionEnvironment(worktree)
	yes := true
	offCfg := agent.SessionConfig{Sandbox: "restricted", SandboxNet: &yes}
	reconcileClearSandbox(&offCfg, offEnv)
	if offCfg.Sandbox != "" || offCfg.SandboxNet != nil {
		t.Errorf("off env must clear the carrier, got Sandbox=%q Net=%v", offCfg.Sandbox, offCfg.SandboxNet)
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
