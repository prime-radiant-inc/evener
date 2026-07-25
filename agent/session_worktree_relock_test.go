package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// These cover the resume re-lock post-init step (auto-delegate-lane-disposal
// spec §P3 "Session resume re-locks its own undisposed lanes"). A clean close
// unlocks a session's KEPT delegate lanes; resume must re-take those locks so
// they are not exposed to another session's P3 residue sweep (which collects
// unlocked delegate lanes).
//
// The subject throughout is serf's lock DECISION — re-lock, adopt, skip, leave
// foreign alone, warn, retry — not git's behavior, so these run on the scripted
// git boundary (scriptedLaneRepo). See docs/testing.md for the rule.

func (r *wtRepo) appendDisposed(t *testing.T, delegateID string) {
	t.Helper()
	if err := r.s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateDisposed,
		TS:         time.Now().UTC(),
		DelegateID: delegateID,
	}); err != nil {
		t.Fatalf("append disposed: %v", err)
	}
}

func TestResumeReLock_UnlockedOwnLaneReLocked(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)

	r.s.resumeReLockOwnLanes()

	_, locked, reason := r.laneLocked(t, path)
	if !locked {
		t.Fatal("resume did not re-lock its own undisposed lane")
	}
	if want := worktree.FormatDelegateMarker(id, r.s.ID()); reason != want {
		t.Errorf("re-lock reason = %q, want own dlg marker %q", reason, want)
	}

	// A P3 residue sweep now skips the re-locked lane (locked → skipped).
	r.ageBeyondGrace(t, id, path)
	r.s.runLaneResidueSweep(context.Background())
	if !r.lanePresent(path) {
		t.Error("P3 sweep collected a resume-re-locked (locked) own lane")
	}
}

// TestResumeReLock_RevivalAdoptsReLockedLane (spec test 30): after resume
// re-locks its own lane, delegate revival classifies it OwnDelegate → adopt (a
// no-op), never a foreign refusal. Verifies the existing lock core, unchanged.
func TestResumeReLock_RevivalAdoptsReLockedLane(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)
	r.s.resumeReLockOwnLanes()

	if err := r.s.reacquireDelegateWorktreeLock(path, id); err != nil {
		t.Fatalf("revival refused a resume-re-locked own lane: %v", err)
	}
	_, locked, reason := r.laneLocked(t, path)
	if !locked || reason != worktree.FormatDelegateMarker(id, r.s.ID()) {
		t.Errorf("revival adopt changed the lock (locked=%t reason=%q)", locked, reason)
	}
}

// TestResumeReLock_DisposedLaneNotReLocked (spec test 30): a Disposed record is
// skipped — resume re-lock never touches a lane whose worktree was already
// removed.
func TestResumeReLock_DisposedLaneNotReLocked(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)
	r.appendDisposed(t, id)

	r.s.resumeReLockOwnLanes()

	_, locked, _ := r.laneLocked(t, path)
	if locked {
		t.Error("resume re-locked a Disposed lane")
	}
}

// TestResumeReLock_DirGoneSkipped (spec test 30): a lane whose directory is gone
// is a clean skip (no re-lock, no warning).
func TestResumeReLock_DirGoneSkipped(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)
	r.removeLane(t, path)
	if r.lanePresent(path) {
		t.Fatalf("lane dir still present after remove")
	}
	_ = id

	r.s.resumeReLockOwnLanes()

	for _, w := range drainBufferedWarnings(r.s) {
		if strings.Contains(w, "could not be re-locked") {
			t.Errorf("dir-gone lane produced a re-lock warning: %q", w)
		}
	}
}

// TestResumeReLock_ForeignLockedUntouched (spec test 30): a lane a foreign owner
// re-locked while it was unlocked is left untouched (never adopted or re-locked),
// with no warning.
func TestResumeReLock_ForeignLockedUntouched(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)
	r.setLaneLock(t, path, "serf:another-session")

	r.s.resumeReLockOwnLanes()

	_, locked, reason := r.laneLocked(t, path)
	if !locked || reason != "serf:another-session" {
		t.Errorf("resume disturbed a foreign lock (locked=%t reason=%q)", locked, reason)
	}
	for _, w := range drainBufferedWarnings(r.s) {
		if strings.Contains(w, "could not be re-locked") {
			t.Errorf("foreign-locked lane produced a re-lock warning: %q", w)
		}
	}
	_ = id
}

// TestResumeReLock_FailureWarnsAndRetriesAtOpenTimer (spec test 30): a re-lock
// failure warns once and is retried at the top-level P3 open timer; a retry that
// then succeeds leaves the lane locked.
func TestResumeReLock_FailureWarnsAndRetriesAtOpenTimer(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	r := newScriptedLaneRepoWithClock(t, clk)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)

	fail, obs := r.failLockRunner()
	fail.Store(true)
	r.s.resumeReLockOwnLanes()
	r.s.armLaneResidueSweepTimer() // top-level: retry piggybacks on the P3 open timer

	warns := drainBufferedWarnings(r.s)
	if countContains(warns, "could not be re-locked on resume") != 1 {
		t.Fatalf("resume re-lock failure warnings = %v, want exactly 1 naming the lane", warns)
	}
	if _, locked, _ := r.laneLocked(t, path); locked {
		t.Fatal("lane was locked despite the injected failure")
	}

	// The lock recovers; the open timer's retry re-locks it.
	fail.Store(false)
	clk.Advance(laneSweepDelay + time.Second)
	waitForCondition(t, 3*time.Second, "open-timer retry re-locks the lane", func() bool {
		_, locked, _ := obs.laneLocked(t, path)
		return locked
	})
	if _, _, reason := obs.laneLocked(t, path); reason != worktree.FormatDelegateMarker(id, r.s.ID()) {
		t.Errorf("retry re-lock reason = %q, want own dlg marker", reason)
	}
}

// TestResumeReLock_SubagentCoordinatorRetryTimer (spec test 30): a restored
// subagent coordinator has no P3 open pass, so a failed re-lock is retried at a
// dedicated one-shot timer.
func TestResumeReLock_SubagentCoordinatorRetryTimer(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	cfg := worktreeTestSessionConfig()
	cfg.clock = clk
	cfg.spawn.parentSessionID = "parent-session"
	r := newScriptedLaneRepoWithConfig(t, cfg)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)

	fail, obs := r.failLockRunner()
	fail.Store(true)
	r.s.resumeReLockOwnLanes()

	r.s.mu.Lock()
	armed := r.s.laneReLockRetryTimer != nil
	sweepArmed := r.s.laneSweepTimer != nil
	r.s.mu.Unlock()
	if !armed {
		t.Fatal("subagent coordinator did not arm the dedicated re-lock retry timer")
	}
	if sweepArmed {
		t.Fatal("subagent coordinator armed a P3 open-pass timer")
	}

	fail.Store(false)
	clk.Advance(laneSweepDelay + time.Second)
	waitForCondition(t, 3*time.Second, "dedicated retry timer re-locks the lane", func() bool {
		_, locked, _ := obs.laneLocked(t, path)
		return locked
	})
	if _, _, reason := obs.laneLocked(t, path); reason != worktree.FormatDelegateMarker(id, r.s.ID()) {
		t.Errorf("retry re-lock reason = %q, want own dlg marker", reason)
	}
}

// TestResumeReLock_StillFailedRetryWarns (spec test 30): a retry that also fails
// emits a warning naming the still-exposed lane.
func TestResumeReLock_StillFailedRetryWarns(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)

	fail, _ := r.failLockRunner()
	fail.Store(true)
	r.s.resumeReLockOwnLanes()
	_ = drainBufferedWarnings(r.s) // discard the first-attempt warning

	r.s.retryPendingReLocks() // fault still active → still fails

	warns := drainBufferedWarnings(r.s)
	if countContains(warns, "could not be re-locked on retry") != 1 {
		t.Fatalf("still-failed retry warnings = %v, want exactly 1 naming the lane", warns)
	}
	if !strings.Contains(strings.Join(warns, "\n"), id) {
		t.Errorf("retry warning does not name the exposed lane %s: %v", id, warns)
	}
	_ = path
}

func countContains(msgs []string, sub string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// TestResumeReLock_NonLocalEnvNoOp: a non-local exec env runs no resume re-lock.
func TestResumeReLock_NonLocalEnvNoOp(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedIsolationLane(t)
	r.unlockLane(t, path)
	r.s.env = &timeoutEnv{wd: r.mainRoot}

	r.s.resumeReLockOwnLanes()

	r.s.env = execenv.NewLocalExecutionEnvironment(r.mainRoot)
	if _, locked, _ := r.laneLocked(t, path); locked {
		t.Error("non-local env ran a resume re-lock")
	}
	_ = id
}
