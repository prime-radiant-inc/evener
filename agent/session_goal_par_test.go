package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// newStateGoalSession builds a session whose StateDir is enabled (so maybeAutoSave
// actually persists) and returns it alongside that dir and a stop func.
func newStateGoalSession(t *testing.T) (*Session, string, func()) {
	t.Helper()
	stateDir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stop := drainEvents(sess)
	return sess, stateDir, func() { sess.Close(); stop() }
}

// TestGoalGateBlockIsPersisted pins /par A4 for the no-progress breaker path: the
// gate flips the goal to blocked AFTER processOneInput's defer-save has already run,
// so only the gate's own maybeAutoSave can persist it. Without that save the goal
// would be saved as still-active and wrongly resume on restart.
func TestGoalGateBlockIsPersisted(t *testing.T) {
	t.Parallel()
	sess, stateDir, stop := newStateGoalSession(t)
	defer stop()

	store := sess.getOrCreateGoalStore()
	store.Set("do the impossible", time.Now())
	// Identical non-advancing continuations graduate nudge-then-block inside
	// the gate (slice-2 ledger bound, K=6 fresh tier: the production digest
	// never moves in this gate-only test). The block lands after
	// processOneInput's defer-save, so only the gate's own save persists it.
	for range goal.RepetitionThresholdFresh - 1 {
		sess.armGoalContinuation(false, true)
	}
	if _, ok := sess.armGoalContinuation(false, true); !ok {
		t.Fatal("K-th identical turn must nudge, not block")
	}
	if _, ok := sess.armGoalContinuation(false, true); ok {
		t.Fatal("post-nudge identical turn must block inside the gate")
	}

	meta, err := schema.LoadSessionMeta(stateDir, sess.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.Goal == nil || meta.Goal.Status != string(goal.StatusBlocked) {
		t.Fatalf("persisted goal = %+v, want status blocked (A4: gate block must be saved)", meta.Goal)
	}
}

// TestGoalErrorBlockIsPersisted pins /par A4 for the error path: terminateGoalOnError
// runs after the defer-save too, so its block must be persisted by its own save.
func TestGoalErrorBlockIsPersisted(t *testing.T) {
	t.Parallel()
	sess, stateDir, stop := newStateGoalSession(t)
	defer stop()

	sess.getOrCreateGoalStore().Set("ship it", time.Now())
	sess.terminateGoalOnError(context.Background(), errors.New("provider exploded"))

	meta, err := schema.LoadSessionMeta(stateDir, sess.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.Goal == nil || meta.Goal.Status != string(goal.StatusBlocked) {
		t.Fatalf("persisted goal = %+v, want status blocked (A4: error block must be saved)", meta.Goal)
	}
	if meta.Goal.StopReason != "provider exploded" {
		t.Fatalf("StopReason = %q, want the error text", meta.Goal.StopReason)
	}
}

// TestGoalRootShutdownLeavesGoalActive pins /par B3 (surfaced by A4): when the
// failure is the root/daemon context being torn down, terminateGoalOnError must
// leave the goal ACTIVE so it resumes on restart, not block it. Without the
// goalRootShutdown discriminator, A4's persist would write a permanent block on
// every shutdown.
func TestGoalRootShutdownLeavesGoalActive(t *testing.T) {
	t.Parallel()
	sess, _, stop := newStateGoalSession(t)
	defer stop()

	sess.getOrCreateGoalStore().Set("long task", time.Now())

	// Simulate the daemon root context shutting down: rootCtx is canceled and the
	// turn surfaces the resulting context.Canceled.
	rootCtx, cancel := context.WithCancel(context.Background())
	cancel()
	turnCtx := WithQueuedInputDrainOnInterrupt(context.Background(), rootCtx)
	sess.terminateGoalOnError(turnCtx, context.Canceled)

	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive {
		t.Fatalf("on root shutdown the goal must stay active, got %+v ok=%v", snap, ok)
	}
}

// TestGoalRestoreOnlyActive pins /par #2 as updated by the goal-wait redesign
// (spec §7): RestoreSessionFromMeta reloads active goals, restores blocked
// goals as dormant-resumable (objective, stopReason, budgets load; no waits,
// no pendingWake, nothing armed, terminal report suppressed — the persisted
// stop doubles as the no-reemit marker), and drops complete goals (reloading
// one would re-emit its terminal report and leave a stale chip).
func TestGoalRestoreOnlyActive(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	now := time.Now()
	metaFor := func(status string) schema.SessionMeta {
		return schema.SessionMeta{
			ID:        "resume-goal",
			ProfileID: "openai",
			Model:     "gpt-5.2",
			Config:    (SessionConfig{}).toSnapshot(),
			Goal: &schema.GoalSnapshot{
				Objective: "finish the migration",
				Status:    status,
				CreatedAt: now,
				UpdatedAt: now,
			},
		}
	}

	// Complete stays dropped: no goal loads, so no terminal report can re-emit.
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), metaFor(string(goal.StatusComplete)), t.TempDir())
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta(complete): %v", err)
	}
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); ok {
		t.Fatal("a complete goal must not be restored (/par #2: complete stays dropped)")
	}
	sess.Close()

	// Blocked restores dormant-resumable: the snapshot loads with its stop
	// reason, but no waits, no backlog, and no re-emitted terminal report.
	sess, err = RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), metaFor(string(goal.StatusBlocked)), t.TempDir())
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta(blocked): %v", err)
	}
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusBlocked || snap.Objective != "finish the migration" {
		t.Fatalf("a blocked goal must restore dormant-resumable, got %+v ok=%v", snap, ok)
	}
	full, ok := sess.getOrCreateGoalStore().GoalSnapshot()
	if !ok {
		t.Fatal("dormant-blocked goal must expose its full snapshot")
	}
	if len(full.Waits) != 0 || len(full.PendingWake) != 0 {
		t.Fatalf("dormant-blocked restore must carry no waits and no backlog, got %+v", full)
	}
	if _, reported := sess.getOrCreateGoalStore().TakeTerminalReport(); !reported {
		t.Fatal("dormant-blocked restore must still own its exactly-once terminal report (suppressed at restore, served on demand — the persisted stop is the no-reemit marker)")
	}
	if _, reported := sess.getOrCreateGoalStore().TakeTerminalReport(); reported {
		t.Fatal("terminal report must emit exactly once after a dormant restore")
	}
	sess.Close()

	sess, err = RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), metaFor(string(goal.StatusActive)), t.TempDir())
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta(active): %v", err)
	}
	defer sess.Close()
	snap, ok = sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive || snap.Objective != "finish the migration" {
		t.Fatalf("active goal must be restored, got %+v ok=%v", snap, ok)
	}
}
