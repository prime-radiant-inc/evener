package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// w3dlg_restoreFixture stands up a resumable stopped-delegate restore fixture
// (child session meta + transcript on disk) and returns the parent session, the
// terminal delegate record, the child id, and a validated restore preflight.
func w3dlg_restoreFixture(t *testing.T) (*Session, *jobstore.JobRecord, string, *delegateRestorePreflight) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	workDir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(workDir)
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { s.close(false) })
	rec := seedStoppedDelegateRestoreRecord(t, s)
	rec.DelegateRestore.WorkingDir = workDir
	replaceStoredDelegateRecord(t, s, rec)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	preflight := requireDelegateRestorePreflight(t, s, rec)
	return s, rec, childID, preflight
}

// TestW3Dlg_RestoreClaimedEnvironmentInvalid covers the child-environment error
// arm: a restore descriptor with an unparseable local_env_policy fails before
// the child session is rebuilt.
func TestW3Dlg_RestoreClaimedEnvironmentInvalid(t *testing.T) {
	t.Parallel()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	rec.DelegateRestore.LocalEnvPolicy = "bogus-policy"

	sub, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if sub != nil || err == nil || !strings.Contains(err.Error(), "local_env_policy") {
		t.Fatalf("sub=%v err=%v, want invalid local_env_policy error", sub, err)
	}
}

// TestW3Dlg_RestoreClaimedFrozenSkillMismatch covers the frozen-skill-body error
// arm: a descriptor whose skill names and bodies disagree fails the restore.
func TestW3Dlg_RestoreClaimedFrozenSkillMismatch(t *testing.T) {
	t.Parallel()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)
	rec.DelegateRestore.FrozenSkillNames = []string{"skill-a"}
	rec.DelegateRestore.FrozenSkillBodies = nil

	sub, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if sub != nil || err == nil {
		t.Fatalf("sub=%v err=%v, want frozen skill mismatch error", sub, err)
	}
}

// TestW3Dlg_RestoreClaimedTrackCollisionReturnsExisting covers the trackIfAbsent
// collision arm: when another runtime claims the child id between reconstruction
// and track, the freshly rebuilt candidate is discarded and the incumbent is
// returned.
func TestW3Dlg_RestoreClaimedTrackCollisionReturnsExisting(t *testing.T) {
	t.Parallel()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)

	incumbentSess := newTestSession(t)
	incumbent := &subagent{
		id:     childID,
		sess:   incumbentSess,
		status: SubagentCompleted,
		done:   make(chan struct{}),
	}
	s.delegateRestoreBeforeTrack = func() {
		s.subagents.track(incumbent)
	}

	sub, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if err != nil {
		t.Fatalf("restoreTerminalDelegateChildClaimed: %v", err)
	}
	if sub != incumbent {
		t.Fatalf("sub = %p, want incumbent %p", sub, incumbent)
	}
}

// TestW3Dlg_RestoreClaimedTrackCollisionUnavailableIncumbent covers the
// collision arm where the incumbent runtime has no live session: the restore
// fails rather than returning an unusable child.
func TestW3Dlg_RestoreClaimedTrackCollisionUnavailableIncumbent(t *testing.T) {
	t.Parallel()
	s, rec, childID, preflight := w3dlg_restoreFixture(t)

	incumbent := &subagent{
		id:     childID,
		sess:   nil, // torn-down runtime retained only as terminal history
		status: SubagentCompleted,
		done:   make(chan struct{}),
	}
	s.delegateRestoreBeforeTrack = func() {
		s.subagents.track(incumbent)
	}
	// Drop the artificial sess-less entry before the fixture's Close runs so
	// session teardown (which closes each retained child) does not dereference
	// the nil session this test deliberately injects.
	t.Cleanup(func() {
		s.subagents.mu.Lock()
		delete(s.subagents.subs, childID)
		s.subagents.mu.Unlock()
	})

	sub, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if sub != nil || err == nil || !strings.Contains(err.Error(), "unavailable retained runtime") {
		t.Fatalf("sub=%v err=%v, want unavailable retained runtime error", sub, err)
	}
}
