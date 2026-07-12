//go:build serffuzz

package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzJobDelegateExactTailCreateRestore retains deterministic coverage for
// create and reconstruction arms that otherwise require narrow runtime faults.
func FuzzJobDelegateExactTailCreateRestore(f *testing.F) {
	for seed := byte(0); seed < 13; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed byte) {
		switch seed % 13 {
		case 0:
			jdTailCreateWorktreeFailure(t)
		case 1:
			jdTailCreatePrepareRollback(t)
		case 2:
			jdTailCreateForegroundTimeout(t)
		case 3:
			jdTailRestoreReasoningAndToolValidation(t)
		case 4:
			jdTailRestoreInvalidChildTools(t)
		case 5:
			jdTailRestoreTrackClosing(t)
		case 6:
			jdTailRestoreSideEffectsClosing(t)
		case 7:
			jdTailRestoreEnvironmentFaults(t)
		case 8:
			jdTailRestoreProfileOverride(t)
		case 9:
			jdTailRestorePendingWait(t)
		case 10:
			jdTailRestoreCollision(t)
		case 11:
			jdTailRestoreSideEffectsWarning(t)
		case 12:
			jdTailRestoreWorktreeReacquire(t)
		}
	})
}

func jdTailCreateWorktreeFailure(t *testing.T) {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newSession(t, withClient(c), withConfig(SessionConfig{MaxSubagentDepth: 1}))
	s.mu.Lock()
	s.env = &timeoutEnv{wd: s.env.WorkingDirectory()}
	s.mu.Unlock()
	res := s.createDelegate(nil, delegateArgs{Task: "isolated", Isolation: "worktree"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "local execution environment") {
		t.Fatalf("worktree failure = %v", res.Err)
	}
}

func jdTailCreatePrepareRollback(t *testing.T) {
	t.Helper()
	rig := newWtDlgRepo(t, delegateTestClient(func(llm.Request) llm.Response {
		return communicateWithDefaultOutput("unused")
	}))
	res := rig.s.createDelegate(nil, delegateArgs{
		Task: "prepare failure", Isolation: "worktree", AgentType: "missing-agent-type",
	})
	if res.Err == nil || res.DelegateID == "" {
		t.Fatalf("prepare rollback = %+v", res)
	}
}

func jdTailCreateForegroundTimeout(t *testing.T) {
	t.Helper()
	release := make(chan struct{})
	c := delegateTestClient(func(llm.Request) llm.Response {
		<-release
		return communicateWithDefaultOutput("late")
	})
	s := newDelegateTestSession(t, c)
	clk := agenttest.NewFakeClock()
	s.cfg.clock = clk
	s.jobManager.clock = clk
	s.jobManager.now = clk.Now
	result := make(chan delegateResult, 1)
	blocked := clk.BlockedCount()
	go func() { result <- s.createDelegate(nil, delegateArgs{Task: "timeout", BlockTimeoutMS: 1}) }()
	clk.BlockUntil(blocked + 1)
	clk.Advance(time.Millisecond)
	res := <-result
	close(release)
	if res.Err != nil || !res.TimedOut || !res.RunningInBackground {
		t.Fatalf("foreground timeout = %+v", res)
	}
}

func jdTailRestoreReasoningAndToolValidation(t *testing.T) {
	t.Helper()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	rec.DelegateRestore.ReasoningEffort = "high"
	rec.DelegateRestore.FrozenToolNames = []string{"read_file"}
	sub, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if err != nil || sub == nil || sub.sess == nil {
		t.Fatalf("restore with required tool = (%v, %v)", sub, err)
	}
	if err := validateRestoredDelegateTools(sub.sess, rec.DelegateRestore); err != nil {
		t.Fatalf("restored tool validation: %v", err)
	}
	leaf := *rec.DelegateRestore
	leaf.DelegationAllowance = 0
	leaf.ParentWatchGranted = true
	leaf.FrozenToolNames = []string{"job_watch"}
	if err := s.validateRestoredDelegateRequiredTools(&leaf); err != nil {
		t.Fatalf("parent-granted job_watch validation: %v", err)
	}
}

func jdTailRestoreInvalidChildTools(t *testing.T) {
	t.Helper()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	rec.DelegateRestore.FrozenToolNames = []string{"read_file"}
	old := delegateRestoreSession
	delegateRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, RestoreSessionConfig) (*Session, error) {
		child := newTestSession(t)
		child.reg = nil
		return child, nil
	}
	t.Cleanup(func() { delegateRestoreSession = old })
	if _, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight); err == nil || !strings.Contains(err.Error(), "registry unavailable") {
		t.Fatalf("invalid child tools error = %v", err)
	}
}

func jdTailRestoreTrackClosing(t *testing.T) {
	t.Helper()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	s.delegateRestoreBeforeTrack = func() {
		s.subagents.mu.Lock()
		s.subagents.closing = true
		s.subagents.mu.Unlock()
	}
	if _, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight); !errors.Is(err, errSubagentManagerClosing) {
		t.Fatalf("track closing error = %v", err)
	}
}

func jdTailRestoreSideEffectsClosing(t *testing.T) {
	t.Helper()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	s.delegateRestoreBeforeSideEffects = func(*Session) {
		s.subagents.mu.Lock()
		s.subagents.closing = true
		s.subagents.mu.Unlock()
	}
	if _, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight); !errors.Is(err, errSubagentManagerClosing) {
		t.Fatalf("side effects closing error = %v", err)
	}
}

func jdTailRestoreEnvironmentFaults(t *testing.T) {
	t.Helper()
	s := &Session{env: execenv.NewLocalExecutionEnvironment(t.TempDir())}
	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: t.TempDir(), LocalEnvPolicy: "default"}
	old := delegateEnableSandbox
	want := errors.New("sandbox enable fault")
	delegateEnableSandbox = func(*execenv.LocalExecutionEnvironment, *sandbox.ResolvedPolicy) error {
		return want
	}
	t.Cleanup(func() { delegateEnableSandbox = old })
	if _, err := s.restoreDelegateChildEnvironment(desc, "dlg_tail"); !errors.Is(err, want) {
		t.Fatalf("sandbox enable error = %v", err)
	}
}

func jdTailRestoreProfileOverride(t *testing.T) {
	t.Helper()
	base := NewOpenAIProfile("base")
	s := &Session{resolveProfile: func(string) (*provider.Profile, error) {
		return NewOpenAIProfile("different"), nil
	}}
	got, err := s.resolveDelegateRestoreProfileRef(base, "other", "model")
	if err != nil || got == nil {
		t.Fatalf("profile override = (%v, %v)", got, err)
	}
}

func jdTailRestorePendingWait(t *testing.T) {
	t.Helper()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	want := &subagent{id: childID, sess: newTestSession(t)}
	pending := &subagentReconstruction{sub: want, done: make(chan struct{})}
	close(pending.done)
	s.subagents.mu.Lock()
	s.subagents.reconstructing[childID] = pending
	s.subagents.mu.Unlock()
	got, err := s.restoreTerminalDelegateChild(rec, childID, preflight)
	if err != nil || got != want {
		t.Fatalf("pending restore = (%p, %v), want %p", got, err, want)
	}
}

func jdTailRestoreCollision(t *testing.T) {
	t.Helper()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	want := &subagent{id: childID, sess: newTestSession(t)}
	s.delegateRestoreBeforeTrack = func() { s.subagents.track(want) }
	got, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if err != nil || got != want {
		t.Fatalf("restore collision = (%p, %v), want %p", got, err, want)
	}
}

func jdTailRestoreSideEffectsWarning(t *testing.T) {
	t.Helper()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	old := delegateRestoreSession
	delegateRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, RestoreSessionConfig) (*Session, error) {
		child := newTestSession(t)
		if err := child.jobManager.store.Close(); err != nil {
			t.Fatal(err)
		}
		return child, nil
	}
	t.Cleanup(func() { delegateRestoreSession = old })
	got, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if err != nil || got == nil {
		t.Fatalf("warning restore = (%v, %v)", got, err)
	}
}

func jdTailRestoreWorktreeReacquire(t *testing.T) {
	t.Helper()
	rig := newWtDlgRepo(t, delegateTestClient(func(llm.Request) llm.Response {
		return communicateWithDefaultOutput("done")
	}))
	res := rig.s.createDelegate(nil, delegateArgs{Task: "lane", Isolation: "worktree", Background: true})
	if res.Err != nil {
		t.Fatalf("create isolated delegate: %v", res.Err)
	}
	lane := rig.lanePath(res.DelegateID)
	if err := rig.s.reacquireDelegateWorktreeLock(lane, res.DelegateID); err != nil {
		t.Fatalf("adopt own lane: %v", err)
	}
	if err := rig.s.reacquireDelegateWorktreeLock(lane, "dlg_other"); err == nil {
		t.Fatal("foreign delegate adopted locked lane")
	}
}
