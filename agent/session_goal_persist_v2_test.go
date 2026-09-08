package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
)

// TestGoalPersistV2_WaitsBudgetsPendingWakeRoundTrip is the Task-4 TDD failing
// test (spec section 7): waits (full predicate payload), budgets (incl.
// maxParkedTotal), and pendingWake survive persist/restore through Meta().
func TestGoalPersistV2_WaitsBudgetsPendingWakeRoundTrip(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	now := clk.Now()
	store := sess.getOrCreateGoalStore()
	store.Set("persist the waits", now)
	w, ok := store.RegisterWait(goal.WaitKind{
		Kind:    goal.WaitUntilTime,
		Target:  "timer-live",
		Timeout: time.Hour,
		Label:   "timer-live",
		Matcher: "m",
	}, now)
	if !ok {
		t.Fatal("precondition: until_time registration should succeed")
	}
	fired, ok := store.RegisterWait(goal.WaitKind{
		Kind:    goal.WaitUntilTime,
		Target:  "timer-fired",
		Timeout: 2 * time.Hour,
		Label:   "timer-fired",
	}, now)
	if !ok {
		t.Fatal("precondition: second until_time registration should succeed")
	}
	firedAt := now.Add(2 * time.Minute)
	if _, ok := store.ClaimFire(fired.Lease.WaitID, "wait expired: timer-fired", firedAt); !ok {
		t.Fatal("precondition: claim should consume the lease")
	}

	meta := sess.Meta()
	if meta.Goal == nil {
		t.Fatal("Meta().Goal must not be nil when a goal with waits is set")
	}
	g := meta.Goal
	if len(g.Waits) != 1 {
		t.Fatalf("persisted waits = %d, want 1 live lease (full predicate payload must survive; the claimed lease leaves the registry)", len(g.Waits))
	}
	got := g.Waits[0]
	if got.WaitID != w.Lease.WaitID || got.Kind != string(goal.WaitUntilTime) || got.Target != "timer-live" || got.Label != "timer-live" || got.Matcher != "m" {
		t.Fatalf("persisted wait = %+v, want the full predicate payload of %+v", got, w.Lease)
	}
	if got.Deadline.IsZero() || got.RegisteredAt.IsZero() || got.IdempotencyKey == "" {
		t.Fatalf("persisted wait = %+v, want deadline/registered_at/idempotency_key carried", got)
	}
	if len(g.PendingWake) != 1 || g.PendingWake[0].WaitID != fired.Lease.WaitID {
		t.Fatalf("persisted pendingWake = %+v, want the one claimed fire", g.PendingWake)
	}
	if g.Budgets == nil {
		t.Fatal("persisted budgets must not be nil (incl. maxParkedTotal)")
	}
	if g.Budgets.MaxContinuations != goal.DefaultMaxContinuations {
		t.Fatalf("MaxContinuations = %d, want default %d", g.Budgets.MaxContinuations, goal.DefaultMaxContinuations)
	}
	if g.Budgets.MaxParkedTotalNanos != int64(goal.DefaultMaxParkedTotal) {
		t.Fatalf("MaxParkedTotalNanos = %d, want default %d", g.Budgets.MaxParkedTotalNanos, int64(goal.DefaultMaxParkedTotal))
	}
	wantDeadline := now.Add(goal.DefaultGoalDeadline)
	if !g.Budgets.Deadline.Equal(wantDeadline) {
		t.Fatalf("Deadline = %v, want SetGoal-anchored %v", g.Budgets.Deadline, wantDeadline)
	}

	// Restore into a fresh store through the schema shape and confirm the
	// gate still sees the backlog (rule 1 drives).
	fresh := goal.NewStore()
	fresh.RestoreSnapshot(goalRestoreToStore(g, firedAt))
	full, ok := fresh.GoalSnapshot()
	if !ok {
		t.Fatal("restored store must have a goal")
	}
	if len(full.Waits) != 1 || len(full.PendingWake) != 1 {
		t.Fatalf("restored waits=%d pendingWake=%d, want 1/1", len(full.Waits), len(full.PendingWake))
	}
	if full.Budgets.MaxContinuations != goal.DefaultMaxContinuations || full.Budgets.MaxParkedTotal != goal.DefaultMaxParkedTotal {
		t.Fatalf("restored budgets = %+v, want defaults carried", full.Budgets)
	}
}
