package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/schema"
)

// TestGoalPersist_MetaPopulated verifies that Meta() includes a populated
// GoalSnapshot when a goal has been set and advanced (non-zero Iterations and
// NoProgressStreak).
func TestGoalPersist_MetaPopulated(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	store := sess.getOrCreateGoalStore()
	store.Set("refactor the world", now)
	// Advance to a progressed state so madeProgressOnce=true and Iterations=2,
	// NoProgressStreak=1.
	store.RecordContinuation(true /*progressed*/, now.Add(time.Minute))
	store.RecordContinuation(false /*progressed*/, now.Add(2*time.Minute))

	meta := sess.Meta()

	if meta.Goal == nil {
		t.Fatal("Meta().Goal must not be nil when a goal is set")
	}
	g := meta.Goal
	if g.Objective != "refactor the world" {
		t.Errorf("Objective: got %q, want %q", g.Objective, "refactor the world")
	}
	if g.Status != string(goal.StatusActive) {
		t.Errorf("Status: got %q, want %q", g.Status, goal.StatusActive)
	}
	if g.Iterations != 2 {
		t.Errorf("Iterations: got %d, want 2", g.Iterations)
	}
	if g.NoProgressStreak != 1 {
		t.Errorf("NoProgressStreak: got %d, want 1", g.NoProgressStreak)
	}
	if !g.MadeProgressOnce {
		t.Error("MadeProgressOnce: got false, want true")
	}
	if !g.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt: got %v, want %v", g.CreatedAt, now)
	}
	if !g.UpdatedAt.Equal(now.Add(2 * time.Minute)) {
		t.Errorf("UpdatedAt: got %v, want %v", g.UpdatedAt, now.Add(2*time.Minute))
	}
}

// TestGoalPersist_NoGoalIsNil verifies that Meta().Goal is nil when no goal is set.
func TestGoalPersist_NoGoalIsNil(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	meta := sess.Meta()
	if meta.Goal != nil {
		t.Fatalf("Meta().Goal must be nil when no goal is set, got %+v", meta.Goal)
	}
}

// TestGoalPersist_RestoreRoundTrip sets a goal, extracts its snapshot via
// Meta(), restores it into a fresh store, and asserts the reconstructed
// Snapshot matches in all observable fields.
func TestGoalPersist_RestoreRoundTrip(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	store := sess.getOrCreateGoalStore()
	store.Set("ship the feature", now)
	store.RecordContinuation(true, now.Add(time.Minute))
	store.RecordContinuation(false, now.Add(2*time.Minute))
	store.RecordContinuation(false, now.Add(3*time.Minute))

	meta := sess.Meta()
	if meta.Goal == nil {
		t.Fatal("Meta().Goal must not be nil")
	}

	// Restore into a fresh store.
	fresh := goal.NewStore()
	g := meta.Goal
	fresh.Restore(g.Objective, g.Status, g.StopReason, g.Iterations, g.NoProgressStreak, g.MadeProgressOnce, g.CreatedAt, g.UpdatedAt)

	snap, ok := fresh.Snapshot()
	if !ok {
		t.Fatal("Restored store must have a goal")
	}
	if snap.Objective != g.Objective {
		t.Errorf("Objective: got %q, want %q", snap.Objective, g.Objective)
	}
	if string(snap.Status) != g.Status {
		t.Errorf("Status: got %q, want %q", snap.Status, g.Status)
	}
	if snap.Iterations != g.Iterations {
		t.Errorf("Iterations: got %d, want %d", snap.Iterations, g.Iterations)
	}
	if snap.NoProgressStreak != g.NoProgressStreak {
		t.Errorf("NoProgressStreak: got %d, want %d", snap.NoProgressStreak, g.NoProgressStreak)
	}
}

// TestGoalPersist_RestorePreservesMadeProgressOnce verifies that after
// Restore, the madeProgressOnce grace flag is correctly reinstated: a
// no-progress turn AFTER restore accrues the streak (not the grace period).
func TestGoalPersist_RestorePreservesMadeProgressOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

	// Build snapshot with madeProgressOnce=true via the schema wire type.
	g := &schema.GoalSnapshot{
		Objective:        "persist and continue",
		Status:           "active",
		Iterations:       1,
		NoProgressStreak: 0,
		MadeProgressOnce: true,
		CreatedAt:        now,
		UpdatedAt:        now.Add(time.Minute),
	}

	fresh := goal.NewStore()
	fresh.Restore(g.Objective, g.Status, g.StopReason, g.Iterations, g.NoProgressStreak, g.MadeProgressOnce, g.CreatedAt, g.UpdatedAt)

	// With madeProgressOnce=true, consecutive no-progress turns must accrue the
	// streak. Fire goal.NoProgressLimit no-progress turns; the store should
	// transition to blocked.
	for i := 0; i < goal.NoProgressLimit; i++ {
		fresh.RecordContinuation(false, now.Add(time.Duration(i+2)*time.Minute))
	}
	snap, ok := fresh.Snapshot()
	if !ok {
		t.Fatal("store should still have a goal")
	}
	if snap.Status != goal.StatusBlocked {
		t.Errorf("Status: got %v, want blocked (streak should have accrued after restore)", snap.Status)
	}
}
