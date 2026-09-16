package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// provisionScratchOwningEnv gives env the owned session scratch a sandboxed
// startup provisions — a write-blocked off policy takes that path without
// needing a kernel backend this host may not have — and returns it. Nothing
// releases the directory or its flock lease until a session owns the
// environment, so every failure between provisioning and that hand-off must
// settle the scratch itself.
func provisionScratchOwningEnv(env *execenv.LocalExecutionEnvironment) error {
	return env.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true})
}

// assertRetainedScratchStillIntact asserts the observable consequence the root
// construction failure must not violate: every reference the root's durable
// retention manifest names still exists at its path, every pin still validates
// and every binding slot resolves to a pinned reference. That is exactly what
// Session.validateRetainedScratchPresent re-checks before a retirement may
// proceed, so a failure here means the root's retirement (and any cold resume
// reacquiring the scratch) would be refused forever.
func assertRetainedScratchStillIntact(t *testing.T, stateDir string, env *execenv.LocalExecutionEnvironment) {
	t.Helper()
	if env == nil {
		t.Fatal("provisioning never ran, so the failed-construction cleanup observed nothing")
	}
	t.Cleanup(env.DisposeUnadoptedScratch)
	binding, err := env.ScratchRetentionBinding()
	if err != nil {
		t.Fatalf("read the env's scratch retention binding after the failed construction: %v", err)
	}
	owner := sandbox.ScratchOwner{StateDir: stateDir, RootSessionID: binding.OwnerSessionID}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("load retention manifest: %v", err)
	}
	if len(manifest.References) == 0 {
		t.Fatal("no scratch reference was ever published; the failed construction observed nothing")
	}
	if _, err := sandbox.ValidateRetainedScratchPins(owner); err != nil {
		t.Fatalf("retained scratch pins no longer validate after the failed construction: %v", err)
	}
	refs := make(map[string]struct{}, len(manifest.References))
	for _, ref := range manifest.References {
		dir, err := filepath.Abs(ref.Dir)
		if err != nil {
			t.Fatalf("resolve retained scratch %q: %v", ref.Dir, err)
		}
		dir = filepath.Clean(dir)
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("retention reference for %q names a missing directory: %v", dir, err)
		}
		refs[dir] = struct{}{}
	}
	for _, b := range manifest.Bindings {
		for kind, slot := range b.Slots {
			dir, err := filepath.Abs(slot.Dir)
			if err != nil {
				t.Fatalf("resolve binding slot %q: %v", slot.Dir, err)
			}
			if _, ok := refs[filepath.Clean(dir)]; !ok {
				t.Fatalf("retained scratch binding %q slot %q references an unpinned directory", b.BindingID, kind)
			}
		}
	}
}

// TestRunRetainsReferencedScratchWhenRootConstructionFailsAfterRetention is the
// root-path twin of the delegate-create regression. `evener run` provisions the
// root's scratch, NewSession publishes durable retention for it
// (installScratchRetention), and then more fallible initialization runs. A
// failure after that publish — here an unknown --context-strategy, which fails
// after installScratchRetention — used to reach the caller's bare
// env.DisposeUnadoptedScratch and os.RemoveAll a directory the manifest still
// referenced. References are append-only with no unpin API, so the root's
// retirement preparation would then refuse forever and cold resume would fail
// the same way. The cleanup must retain a referenced allocation instead.
func TestRunRetainsReferencedScratchWhenRootConstructionFailsAfterRetention(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldEnsure := runEnsureUserConfigDirs
	t.Cleanup(func() { runEnsureUserConfigDirs = oldEnsure })
	runEnsureUserConfigDirs = func() error { return nil }

	oldProvision := runProvisionSandbox
	t.Cleanup(func() { runProvisionSandbox = oldProvision })
	var provisioned *execenv.LocalExecutionEnvironment
	runProvisionSandbox = func(env *execenv.LocalExecutionEnvironment, _ *agent.SessionConfig, _ string) error {
		provisioned = env
		return provisionScratchOwningEnv(env)
	}

	dir := t.TempDir()
	err := run(context.Background(), runConfig{
		prompt: "hello", model: "openai/gpt-test", workDir: dir, stateDir: dir,
		noDefaultMarketplaces: true, contextStrategy: "definitely-not-a-strategy",
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown context strategy") {
		t.Fatalf("run error = %v, want the post-retention context-strategy failure", err)
	}

	assertRetainedScratchStillIntact(t, dir, provisioned)
}

// TestServeRetainsReferencedScratchWhenRootConstructionFailsAfterRetention is
// the serve-startup half of the same regression: `evener serve` reaches its own
// bare env.DisposeUnadoptedScratch on a failed fresh-session construction and
// owes the same retention-aware cleanup.
func TestServeRetainsReferencedScratchWhenRootConstructionFailsAfterRetention(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	var provisioned *execenv.LocalExecutionEnvironment
	deps.provisionSandbox = func(env *execenv.LocalExecutionEnvironment, _ *agent.SessionConfig, _ string) error {
		provisioned = env
		return provisionScratchOwningEnv(env)
	}

	stateDir := t.TempDir()
	err := runServeWithDeps([]string{
		"--model", "openai/gpt-test", "--dir", t.TempDir(), "--state-dir", stateDir,
		"--context-strategy", "definitely-not-a-strategy",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "unknown context strategy") {
		t.Fatalf("serve error = %v, want the post-retention context-strategy failure", err)
	}

	assertRetainedScratchStillIntact(t, stateDir, provisioned)
}

// TestRunServeClearRetainsReferencedScratchWhenConstructionFailsAfterRetention
// is the thread/clear half of the same regression. The clear path builds its
// replacement through the same agent.NewSession, which publishes the root's
// durable scratch retention partway through construction, and then reaches its
// own bare clearEnv.DisposeUnadoptedScratch. A failure after that publish would
// os.RemoveAll a directory the manifest still references; references are
// append-only with no unpin API, so the root's retirement preparation and any
// cold resume would then be refused forever. The cleanup owes the same
// retention-aware settling the fresh-session path already does.
func TestRunServeClearRetainsReferencedScratchWhenConstructionFailsAfterRetention(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	var provisioned *execenv.LocalExecutionEnvironment
	var stateDir string
	deps.provisionSandbox = func(env *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, _ string) error {
		provisioned = env
		if cfg != nil {
			stateDir = cfg.StateDir
		}
		return provisionScratchOwningEnv(env)
	}
	buildClearSession := deps.newClearSession
	deps.newClearSession = func(c *llm.Client, p *provider.Profile, e execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		// Fail AFTER installScratchRetention published the retained allocation,
		// which is the same late failure the run and serve-fresh regression
		// tests drive: the strategy is resolved after retention is published.
		cfg.ContextStrategy = "definitely-not-a-strategy"
		return buildClearSession(c, p, e, cfg)
	}

	obs := runClearAttempt(t, deps, state, args, nil)
	if obs.clearErr == nil || !strings.Contains(obs.clearErr.Error(), "unknown context strategy") {
		t.Fatalf("thread/clear error = %v, want the post-retention context-strategy failure", obs.clearErr)
	}
	if stateDir == "" || provisioned == nil {
		t.Fatal("the clear construction never provisioned, so the failed cleanup observed nothing")
	}

	assertRetainedScratchStillIntact(t, stateDir, provisioned)
}
