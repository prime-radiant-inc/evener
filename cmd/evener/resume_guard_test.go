package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// seedDelegateMeta writes the metadata a live delegate leaves on disk: the
// persisted IsSubagent flag with the spawn parent's session id. The parent
// carrier (cfg.spawn) is never persisted, so a bare resume of this id arrives
// with an empty carrier.
func seedDelegateMeta(t *testing.T, stateDir, id, parent string) {
	t.Helper()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: id, ProfileID: "openai", Model: "gpt-test",
		IsSubagent: true, ParentSessionID: parent,
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
}

// errRestoreReached is the error a stub restore returns so an UNGUARDED resume
// returns promptly instead of provisioning a session around a delegate.
var errRestoreReached = errors.New("restore reached: the guard did not refuse the delegate resume")

func failRestore(t *testing.T) func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
	t.Helper()
	return func(_ *llm.Client, _ *provider.Profile, _ execenv.ExecutionEnvironment, meta schema.SessionMeta, _ agent.RestoreSessionConfig) (*agent.Session, error) {
		t.Errorf("restore ran for delegate %s: the guard must refuse before any restore", meta.ID)
		return nil, errRestoreReached
	}
}

// TestServeResumeOfDelegateIsRefused pins #4442's ruling at the operation the
// user types: `evener serve --resume <delegate-id>` must fail loudly instead of
// restoring the delegate as a root runtime with a root delegation allowance and
// the delegate tool. The restore is stubbed to fail, so the test also proves
// the refusal fires before the restore is reached.
func TestServeResumeOfDelegateIsRefused(t *testing.T) {
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.restoreSession = failRestore(t)

	stateDir := resolvedTempDir(t)
	id := identifier.MustNewSessionID()
	seedDelegateMeta(t, stateDir, id, identifier.MustNewSessionID())

	err := runServeWithDeps([]string{
		"--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir(),
		"--resume", id,
	}, deps)
	if !errors.Is(err, errBareDelegateResume) {
		t.Fatalf("serve --resume of a delegate = %v, want errBareDelegateResume", err)
	}
}

// TestRunResumeOfDelegateIsRefused pins the same ruling on the default command:
// `evener --resume <delegate-id>` restores through the same path and must fail
// the same way, before the restore is reached.
func TestRunResumeOfDelegateIsRefused(t *testing.T) {
	t.Setenv("TMPDIR", resolvedTempDir(t))
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldEnsure, oldRestore := runEnsureUserConfigDirs, runRestoreSession
	t.Cleanup(func() { runEnsureUserConfigDirs, runRestoreSession = oldEnsure, oldRestore })
	runEnsureUserConfigDirs = func() error { return nil }
	runRestoreSession = failRestore(t)

	stateDir := resolvedTempDir(t)
	id := identifier.MustNewSessionID()
	seedDelegateMeta(t, stateDir, id, identifier.MustNewSessionID())

	err := run(context.Background(), runConfig{
		workDir: stateDir, stateDir: stateDir, noDefaultMarketplaces: true, resume: id,
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if !errors.Is(err, errBareDelegateResume) {
		t.Fatalf("run --resume of a delegate = %v, want errBareDelegateResume", err)
	}
}

// TestRefuseBareDelegateResumeAllowsRootAndFork pins the paths the refusal must
// NOT touch: a root session never carries the flag, and a fork carries
// ParentSessionID with IsSubagent false (fork lineage is a different concept
// from is_subagent, agent/session_tools_ask.go).
func TestRefuseBareDelegateResumeAllowsRootAndFork(t *testing.T) {
	root := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB"}
	if err := refuseBareDelegateResume(root, ""); err != nil {
		t.Errorf("root resume refused: %v", err)
	}
	fork := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeC", ParentSessionID: root.ID}
	if err := refuseBareDelegateResume(fork, ""); err != nil {
		t.Errorf("fork resume refused: %v", err)
	}
}

// TestRefuseBareDelegateResumeMessage pins the whole error text, not a
// substring (docs/developing-evener/testing.md). A delegate's parent may itself
// be a delegate (also refused), so the message must name the ROOT session to
// resume — the top of the persisted owner chain — and route follow-up through
// the delegate's owner rather than promise a bare delegate_send (roborev, PR
// #4589).
func TestRefuseBareDelegateResumeMessage(t *testing.T) {
	stateDir := resolvedTempDir(t)
	// A three-hop chain: root (not a delegate) <- mid delegate <- leaf delegate.
	const root, mid, leaf = "02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv1C3Hut0M8GCeC", "02wMz5Txv1C3Hut0M8GCeD"
	save := func(id, parent string, isSubagent bool) {
		t.Helper()
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
			ID: id, ProfileID: "openai", Model: "gpt-test", IsSubagent: isSubagent, ParentSessionID: parent,
		}); err != nil {
			t.Fatalf("save meta %s: %v", id, err)
		}
	}
	save(root, "", false)
	save(mid, root, true)
	save(leaf, mid, true)

	nested := refuseBareDelegateResume(schema.SessionMeta{ID: leaf, IsSubagent: true, ParentSessionID: mid}, stateDir)
	if nested == nil {
		t.Fatal("nested delegate was not refused")
	}
	checkResumeGuardGolden(t, "resume_guard_delegate_with_root.golden", nested.Error())

	orphan := refuseBareDelegateResume(schema.SessionMeta{
		ID: "02wMz5Txv1C3Hut0M8GCeE", ProfileID: "openai", Model: "gpt-test", IsSubagent: true,
	}, stateDir)
	if orphan == nil {
		t.Fatal("delegate with no owner chain was not refused")
	}
	checkResumeGuardGolden(t, "resume_guard_delegate_without_root.golden", orphan.Error())
}

// updateResumeGuardGolden rewrites the goldens under testdata/, the same
// regeneration shape docs/developing-evener/testing.md names for golden prose.
var updateResumeGuardGolden = flag.Bool("update-resume-guard-golden", false, "rewrite testdata/resume_guard_*.golden")

// checkResumeGuardGolden compares got against testdata/name as one whole string.
func checkResumeGuardGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateResumeGuardGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v; regenerate with go test ./cmd/evener -run TestRefuseBareDelegateResumeMessage -update-resume-guard-golden", path, err)
	}
	if string(want) != got {
		t.Fatalf("%s drifted.\n got: %q\nwant: %q", path, got, string(want))
	}
}
