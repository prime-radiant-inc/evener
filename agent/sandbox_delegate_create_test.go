package agent

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

// sandboxScratchDirs lists the per-session sandbox scratch dirs (serf-sandbox-*)
// directly under base — the leak surface for a per-delegate sandbox whose spawn
// fails after EnableSandbox provisioned one.
func sandboxScratchDirs(t *testing.T, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read tmp base %q: %v", base, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "serf-sandbox-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// sbxSetParentMode sets the session's currentEnv() to a resolved sandbox policy of
// the given mode (no kernel wrapper — the floor path only reads mode/network), so a
// createDelegate floor test can run under a concrete parent box.
func sbxSetParentMode(t *testing.T, s *Session, facts sandbox.HostFacts, cwd string, mode sandbox.Mode) {
	t.Helper()
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("resolve parent mode %v: %v", mode, err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
}

// prepareWithDelegateSandbox threads a per-delegate sandbox policy (and a lane
// working dir) through prepareSubagentRun, returning the prepared run. The caller
// owns cleanup.
func prepareWithDelegateSandbox(t *testing.T, s *Session, lane string, pol *sandbox.SandboxPolicy) *preparedSubagentRun {
	t.Helper()
	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	ctx = context.WithValue(ctx, ctxParentJobID, "job_sbx")
	ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, pol)
	prepared, err := s.prepareSubagentRun(ctx, "child task", "", lane, 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	t.Cleanup(func() {
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
	})
	return prepared
}

// TestPrepareSubagentRun_PerDelegateSandboxEnforcedAndPersisted: a delegate under an
// OFF parent that requests its own restricted box gets an env enforced at ITS lane,
// and the persisted descriptor carries the REQUESTED box (not the parent's off).
func TestPrepareSubagentRun_PerDelegateSandboxEnforcedAndPersisted(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts) // parent env is off

	prepared := prepareWithDelegateSandbox(t, s, lane, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(true)})

	// Persisted snapshot reflects the requested box.
	if prepared.sandboxSnapshot == nil || prepared.sandboxSnapshot.Mode != "restricted" {
		t.Fatalf("prepared snapshot must reflect the requested restricted box, got %+v", prepared.sandboxSnapshot)
	}
	if prepared.sandboxSnapshot.Network == nil || !*prepared.sandboxSnapshot.Network {
		t.Errorf("prepared snapshot must persist net on, got %+v", prepared.sandboxSnapshot.Network)
	}

	// The live child env is enforced restricted, anchored at its own lane.
	le, ok := prepared.sub.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("child env must be a LocalExecutionEnvironment")
	}
	if le.Sandbox == nil || !le.Sandbox.Enforced() || le.Sandbox.Mode != sandbox.ModeRestricted {
		t.Fatalf("child env must be enforced restricted, got %+v", le.Sandbox)
	}
	if le.Sandbox.Git.WorktreeRoot != lane {
		t.Errorf("child box must anchor at the lane %q, got %q", lane, le.Sandbox.Git.WorktreeRoot)
	}
	if le.Wrapper == nil {
		t.Error("an enforced child box must provision a kernel wrapper")
	}

	// The persisted DelegateRestoreDescriptor carries the requested box.
	desc := s.delegateRestoreDescriptor("job_sbx", prepared.sub.id, "child task", encodeRef("", prepared.sub.id), nil, prepared)
	if desc.Sandbox == nil || desc.Sandbox.Mode != "restricted" {
		t.Errorf("persisted descriptor must carry the requested restricted box, got %+v", desc.Sandbox)
	}
}

// TestPrepareSubagentRun_PerDelegateSandboxOverridesSandboxedParent: a tighter
// per-delegate box (restricted) OVERRIDES a looser sandboxed parent (workspace-write
// + an out-of-lane extra writable root). The child is restricted and the parent's
// extra writable root does NOT leak onto it — the delegate's box is a pure function
// of ITS OWN policy.
func TestPrepareSubagentRun_PerDelegateSandboxOverridesSandboxedParent(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	parentExtra := sbxSandboxedParent(t, s, facts, lane) // parent is workspace-write + extra root

	prepared := prepareWithDelegateSandbox(t, s, lane, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(true)})

	le := prepared.sub.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if le.Sandbox == nil || le.Sandbox.Mode != sandbox.ModeRestricted {
		t.Fatalf("child must be restricted (tighter than the workspace-write parent), got %+v", le.Sandbox)
	}
	if slices.Contains(le.Sandbox.FileTool.WriteRoots, parentExtra) {
		t.Errorf("the parent's extra writable root %q leaked onto the delegate: %v", parentExtra, le.Sandbox.FileTool.WriteRoots)
	}
	if prepared.sandboxSnapshot == nil || prepared.sandboxSnapshot.Mode != "restricted" {
		t.Errorf("persisted snapshot must be the delegate's restricted box, not the parent's: %+v", prepared.sandboxSnapshot)
	}
}

// TestCreateDelegate_SandboxFloorRefusedEarly: a delegate that requests a LOOSER box
// than its parent is refused with a legible invalid_request error, and the refusal
// happens BEFORE minting any IDs (no delegate id is returned).
func TestCreateDelegate_SandboxFloorRefusedEarly(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	sbxSetParentMode(t, s, facts, lane, sandbox.ModeRestricted) // parent restricted

	res := s.createDelegate(context.Background(), delegateArgs{Task: "do work", Sandbox: "off"})
	if res.Err == nil {
		t.Fatal("a looser delegate box under a restricted parent must be refused")
	}
	if !strings.Contains(res.Err.Error(), "invalid_request:") {
		t.Errorf("refusal must be an invalid_request error, got %v", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "grants more access than your own sandbox") {
		t.Errorf("refusal must explain the escalation, got %v", res.Err)
	}
	if res.DelegateID != "" {
		t.Errorf("floor refusal must not mint a delegate id, got %q", res.DelegateID)
	}
}

// TestPrepareSubagentRun_PerDelegateSandboxCleansScratchOnSpawnFailure: when a
// per-delegate sandbox EnableSandbox's a fresh env and the spawn then fails at
// NewSession, the provisioned scratch dir must be disposed, not leaked. A nil child
// client (via childClientFactory) forces NewSession to fail AFTER EnableSandbox. Not
// parallel: it isolates TMPDIR to observe the sandbox scratch base.
func TestPrepareSubagentRun_PerDelegateSandboxCleansScratchOnSpawnFailure(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)

	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newSession(t, withClient(c), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-leak-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: facts},
			childClientFactory:  func() *llm.Client { return nil },
		},
	}))

	if before := sandboxScratchDirs(t, isolated); len(before) != 0 {
		t.Fatalf("isolated tmp base must start free of sandbox scratch, got %v", before)
	}

	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	ctx = context.WithValue(ctx, ctxParentJobID, "job_leak")
	ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(true)})

	prepared, err := s.prepareSubagentRun(ctx, "child task", "", lane, 0, "", "", nil, nil)
	if err == nil {
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
		t.Fatal("expected NewSession to fail with a nil child client")
	}
	if left := sandboxScratchDirs(t, isolated); len(left) != 0 {
		t.Errorf("per-delegate sandbox scratch leaked on spawn failure: %v", left)
	}
}

// TestCreateDelegate_SandboxNetWithoutModeRefusedEarly: setting sandbox_net alone
// under an unsandboxed parent is refused with a legible error (not silently
// dropped), before any IDs are minted.
func TestCreateDelegate_SandboxNetWithoutModeRefusedEarly(t *testing.T) {
	_, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts) // parent env is off

	res := s.createDelegate(context.Background(), delegateArgs{Task: "do work", SandboxNet: boolPtr(false)})
	if res.Err == nil {
		t.Fatal("sandbox_net without a mode under an unsandboxed parent must be refused")
	}
	if !strings.Contains(res.Err.Error(), "invalid_request:") || !strings.Contains(res.Err.Error(), "requires a sandbox mode") {
		t.Errorf("refusal must explain sandbox_net requires a sandbox mode, got %v", res.Err)
	}
	if res.DelegateID != "" {
		t.Errorf("refusal must not mint a delegate id, got %q", res.DelegateID)
	}
}

// TestPerDelegateSandbox_CreateResumeRoundTrip: a delegate created with its own
// explicit box (restricted, net off) persists that box, and on RESTORE re-resolves
// the SAME box against its lane — independent of the parent, which by resume time is
// a DIFFERENT (workspace-write) sandbox. The parent's extra writable root never
// leaks into the resumed delegate.
func TestPerDelegateSandbox_CreateResumeRoundTrip(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts) // parent off at create time

	prepared := prepareWithDelegateSandbox(t, s, lane, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(false)})
	if prepared.sandboxSnapshot == nil || prepared.sandboxSnapshot.Mode != "restricted" ||
		prepared.sandboxSnapshot.Network == nil || *prepared.sandboxSnapshot.Network {
		t.Fatalf("create must persist restricted + net off, got %+v", prepared.sandboxSnapshot)
	}
	desc := s.delegateRestoreDescriptor("job_sbx", prepared.sub.id, "child task", encodeRef("", prepared.sub.id), nil, prepared)

	// At resume time the parent is a DIFFERENT, looser sandbox.
	parentExtra := sbxSandboxedParent(t, s, facts, lane)

	childEnv, err := s.restoreDelegateChildEnvironment(desc, "dlg_rt")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	le := childEnv.(*execenv.LocalExecutionEnvironment)
	if le.Sandbox == nil || le.Sandbox.Mode != sandbox.ModeRestricted {
		t.Fatalf("resumed delegate must keep its OWN restricted box, got %+v", le.Sandbox)
	}
	if le.Sandbox.Network {
		t.Error("resumed delegate must keep its persisted net-off, got net on")
	}
	if slices.Contains(le.Sandbox.FileTool.WriteRoots, parentExtra) {
		t.Errorf("the resume-time parent's extra writable root %q leaked into the resumed delegate: %v", parentExtra, le.Sandbox.FileTool.WriteRoots)
	}
}
