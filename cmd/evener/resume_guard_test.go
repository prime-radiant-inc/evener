package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// saveMeta writes a session meta into stateDir. UpdatedAt orders the implicit
// --resume-last pick.
func saveMeta(t *testing.T, stateDir string, meta schema.SessionMeta) {
	t.Helper()
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta(%s): %v", meta.ID, err)
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
	saveMeta(t, stateDir, schema.SessionMeta{
		ID: id, ProfileID: "openai", Model: "gpt-test",
		IsSubagent: true, ParentSessionID: identifier.MustNewSessionID(),
	})

	err := runServeWithDeps([]string{
		"--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir(),
		"--resume", id,
	}, deps)
	if !errors.Is(err, errBareDelegateResume) {
		t.Fatalf("serve --resume of a delegate = %v, want errBareDelegateResume", err)
	}
}

// TestServeResumeOfNestedDelegateIsRefused covers a delegate whose parent is
// itself a delegate: an explicit id is refused the same way, and the refusal
// names the root of the owner chain rather than the parent (roborev, PR #4589).
func TestServeResumeOfNestedDelegateIsRefused(t *testing.T) {
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.restoreSession = failRestore(t)

	stateDir := resolvedTempDir(t)
	root, mid, leaf := identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID()
	saveMeta(t, stateDir, schema.SessionMeta{ID: root, ProfileID: "openai", Model: "gpt-test"})
	saveMeta(t, stateDir, schema.SessionMeta{ID: mid, ProfileID: "openai", Model: "gpt-test", IsSubagent: true, ParentSessionID: root})
	saveMeta(t, stateDir, schema.SessionMeta{ID: leaf, ProfileID: "openai", Model: "gpt-test", IsSubagent: true, ParentSessionID: mid})

	err := runServeWithDeps([]string{
		"--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir(),
		"--resume", leaf,
	}, deps)
	if !errors.Is(err, errBareDelegateResume) {
		t.Fatalf("serve --resume of a nested delegate = %v, want errBareDelegateResume", err)
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
	saveMeta(t, stateDir, schema.SessionMeta{
		ID: id, ProfileID: "openai", Model: "gpt-test",
		IsSubagent: true, ParentSessionID: identifier.MustNewSessionID(),
	})

	err := run(context.Background(), runConfig{
		workDir: stateDir, stateDir: stateDir, noDefaultMarketplaces: true, resume: id,
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if !errors.Is(err, errBareDelegateResume) {
		t.Fatalf("run --resume of a delegate = %v, want errBareDelegateResume", err)
	}
}

// seedNewerDelegateAndOlderRoot writes an older resumable root and a newer
// delegate, and asserts the implicit --resume-last pick really is the delegate,
// so the fixture exercises the case roborev flagged.
func seedNewerDelegateAndOlderRoot(t *testing.T, stateDir string) (root, delegate string) {
	t.Helper()
	root, delegate = identifier.MustNewSessionID(), identifier.MustNewSessionID()
	saveMeta(t, stateDir, schema.SessionMeta{
		ID: root, ProfileID: "openai", Model: "gpt-test", UpdatedAt: time.Unix(100, 0).UTC(),
	})
	saveMeta(t, stateDir, schema.SessionMeta{
		ID: delegate, ProfileID: "openai", Model: "gpt-test", IsSubagent: true,
		ParentSessionID: root, UpdatedAt: time.Unix(200, 0).UTC(),
	})
	newest, err := cmdutil.ResolveSessionMeta(stateDir, "", true)
	if err != nil {
		t.Fatalf("ResolveSessionMeta(resume-last): %v", err)
	}
	if newest.ID != delegate {
		t.Fatalf("test setup: implicit pick = %s, want the newer delegate %s", newest.ID, delegate)
	}
	return root, delegate
}

// TestServeResumeLastResolvesToResumableRoot pins that an implicit
// `serve --resume-last` whose newest session is a delegate resumes the newest
// session that CAN be resumed, rather than refusing (roborev, PR #4589).
func TestServeResumeLastResolvesToResumableRoot(t *testing.T) {
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	var gotID string
	deps.restoreSession = func(_ *llm.Client, _ *provider.Profile, _ execenv.ExecutionEnvironment, meta schema.SessionMeta, _ agent.RestoreSessionConfig) (*agent.Session, error) {
		gotID = meta.ID
		return nil, errRestoreReached
	}

	stateDir := resolvedTempDir(t)
	root, _ := seedNewerDelegateAndOlderRoot(t, stateDir)

	err := runServeWithDeps([]string{
		"--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir(),
		"--resume-last",
	}, deps)
	if errors.Is(err, errBareDelegateResume) {
		t.Fatalf("serve --resume-last refused although root %s can be resumed", root)
	}
	if gotID != root {
		t.Fatalf("serve --resume-last resumed %q, want the newest resumable root %q", gotID, root)
	}
}

// TestRunResumeLastResolvesToResumableRoot is the same pin on the default
// command.
func TestRunResumeLastResolvesToResumableRoot(t *testing.T) {
	t.Setenv("TMPDIR", resolvedTempDir(t))
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldEnsure, oldRestore := runEnsureUserConfigDirs, runRestoreSession
	t.Cleanup(func() { runEnsureUserConfigDirs, runRestoreSession = oldEnsure, oldRestore })
	runEnsureUserConfigDirs = func() error { return nil }
	var gotID string
	runRestoreSession = func(_ *llm.Client, _ *provider.Profile, _ execenv.ExecutionEnvironment, meta schema.SessionMeta, _ agent.RestoreSessionConfig) (*agent.Session, error) {
		gotID = meta.ID
		return nil, errRestoreReached
	}

	stateDir := resolvedTempDir(t)
	root, _ := seedNewerDelegateAndOlderRoot(t, stateDir)

	err := run(context.Background(), runConfig{
		workDir: stateDir, stateDir: stateDir, noDefaultMarketplaces: true, resumeLast: true,
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if errors.Is(err, errBareDelegateResume) {
		t.Fatalf("run --resume-last refused although root %s can be resumed", root)
	}
	if gotID != root {
		t.Fatalf("run --resume-last resumed %q, want the newest resumable root %q", gotID, root)
	}
}

// TestRefuseBareDelegateResumeAllowsRootAndFork pins the paths the refusal must
// NOT touch: a root session never carries the flag, and a fork carries
// ParentSessionID with IsSubagent false (fork lineage is a different concept
// from is_subagent, agent/session_tools_ask.go).
func TestRefuseBareDelegateResumeAllowsRootAndFork(t *testing.T) {
	root := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	if err := refuseBareDelegateResume(root, ""); err != nil {
		t.Errorf("root resume refused: %v", err)
	}
	fork := schema.SessionMeta{ID: identifier.MustNewSessionID(), ParentSessionID: root.ID}
	if err := refuseBareDelegateResume(fork, ""); err != nil {
		t.Errorf("fork resume refused: %v", err)
	}
}

// TestDelegateRootOwner pins the structured walk: it returns the session id a
// reader should resume, one hop or many, and "" when it cannot vouch for one.
func TestDelegateRootOwner(t *testing.T) {
	stateDir := resolvedTempDir(t)
	root, mid, leaf := identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID()
	saveMeta(t, stateDir, schema.SessionMeta{ID: root})
	saveMeta(t, stateDir, schema.SessionMeta{ID: mid, IsSubagent: true, ParentSessionID: root})
	saveMeta(t, stateDir, schema.SessionMeta{ID: leaf, IsSubagent: true, ParentSessionID: mid})

	cases := []struct {
		name string
		meta schema.SessionMeta
		want string
	}{
		{"three hops", schema.SessionMeta{ID: leaf, IsSubagent: true, ParentSessionID: mid}, root},
		{"one hop", schema.SessionMeta{ID: mid, IsSubagent: true, ParentSessionID: root}, root},
		{"missing ancestor", schema.SessionMeta{ID: leaf, IsSubagent: true, ParentSessionID: identifier.MustNewSessionID()}, ""},
		{"no parent id", schema.SessionMeta{ID: leaf, IsSubagent: true}, ""},
		{"no state dir", schema.SessionMeta{ID: leaf, IsSubagent: true, ParentSessionID: mid}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := stateDir
			if tc.name == "no state dir" {
				dir = ""
			}
			if got := delegateRootOwner(tc.meta, dir); got != tc.want {
				t.Fatalf("delegateRootOwner = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDelegateRootOwnerStopsAtFork pins that a fork stops the walk: its
// ParentSessionID is lineage, not ownership, so the fork itself is the session
// to resume and walking past it would name the fork's source.
func TestDelegateRootOwnerStopsAtFork(t *testing.T) {
	stateDir := resolvedTempDir(t)
	source, fork, child := identifier.MustNewSessionID(), identifier.MustNewSessionID(), identifier.MustNewSessionID()
	saveMeta(t, stateDir, schema.SessionMeta{ID: source})
	saveMeta(t, stateDir, schema.SessionMeta{ID: fork, ParentSessionID: source}) // a fork: ParentSessionID set, IsSubagent false
	saveMeta(t, stateDir, schema.SessionMeta{ID: child, IsSubagent: true, ParentSessionID: fork})

	if got := delegateRootOwner(schema.SessionMeta{ID: child, IsSubagent: true, ParentSessionID: fork}, stateDir); got != fork {
		t.Fatalf("delegateRootOwner = %q, want the fork %q (lineage must not be walked past)", got, fork)
	}
}
