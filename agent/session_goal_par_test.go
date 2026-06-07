package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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
	sess, stateDir, stop := newStateGoalSession(t)
	defer stop()

	store := sess.getOrCreateGoalStore()
	store.Set("do the impossible", time.Now())
	// One progressed turn, then NoProgressLimit no-progress continuations: the last
	// blocks the goal inside the gate.
	if _, ok := sess.armGoalContinuation(true, true); !ok {
		t.Fatal("first progressed continuation should keep the goal active")
	}
	for i := 0; i < goal.NoProgressLimit; i++ {
		sess.armGoalContinuation(false, true)
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

// TestGoalRestoreOnlyActive pins /par #2: RestoreSessionFromMeta reloads only an
// active goal. A complete/blocked goal is dropped — re-restoring it would re-emit
// its terminal report (the once-gate resets on load) and leave a stale chip.
func TestGoalRestoreOnlyActive(t *testing.T) {
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

	for _, status := range []string{string(goal.StatusComplete), string(goal.StatusBlocked)} {
		sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), metaFor(status), t.TempDir())
		if err != nil {
			t.Fatalf("RestoreSessionFromMeta(%s): %v", status, err)
		}
		if _, ok := sess.getOrCreateGoalStore().Snapshot(); ok {
			t.Fatalf("a %s goal must not be restored (/par #2)", status)
		}
		sess.Close()
	}

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), metaFor(string(goal.StatusActive)), t.TempDir())
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta(active): %v", err)
	}
	defer sess.Close()
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive || snap.Objective != "finish the migration" {
		t.Fatalf("active goal must be restored, got %+v ok=%v", snap, ok)
	}
}
