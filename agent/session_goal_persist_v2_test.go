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

// TestGoalPersistV2_ConditionsAndWakeKindRoundTrip pins fix-1/4 m3: registered
// Conditions and the structural PendingWake.Kind survive persist/restore
// through the schema converters (Meta → store).
func TestGoalPersistV2_ConditionsAndWakeKindRoundTrip(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	now := clk.Now()
	store := sess.getOrCreateGoalStore()
	store.Set("persist the conditions", now)
	store.SetSubstrate(&goalPersistCondSubstrate{files: map[string]string{"/work/spec.md": "v1"}})
	if _, ok := store.RegisterExpect(goal.ExpectRequest{
		Desc:      "spec changed",
		Predicate: goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventFileModified, Target: "/work/spec.md", Timeout: 5 * time.Minute},
	}, now); !ok {
		t.Fatalf("precondition: file condition must register: %q", store.LastRejectReason())
	}
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "timer"}, now)
	if !ok {
		t.Fatal("precondition: until_time registration should succeed")
	}
	firedAt := now.Add(time.Minute)
	entry, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: timer", firedAt)
	if !ok {
		t.Fatal("precondition: claim should consume the lease")
	}
	if entry.Kind != goal.WaitUntilTime {
		t.Fatalf("claim kind = %q, want until_time", entry.Kind)
	}

	meta := sess.Meta()
	if meta.Goal == nil {
		t.Fatal("Meta().Goal must not be nil")
	}
	g := meta.Goal
	if len(g.Conditions) != 1 || g.Conditions[0].Desc != "spec changed" {
		t.Fatalf("persisted conditions = %+v, want the one file condition", g.Conditions)
	}
	c := g.Conditions[0]
	if c.Kind != string(goal.WaitUntilEvent) || c.Target != "/work/spec.md" || c.Baseline != "v1" {
		t.Fatalf("persisted condition = %+v, want the full predicate + baseline", c)
	}
	if len(g.PendingWake) != 1 || g.PendingWake[0].Kind != string(goal.WaitUntilTime) {
		t.Fatalf("persisted pendingWake = %+v, want the structural kind carried", g.PendingWake)
	}

	fresh := goal.NewStore()
	fresh.RestoreSnapshot(goalRestoreToStore(g, firedAt))
	full, ok := fresh.GoalSnapshot()
	if !ok {
		t.Fatal("restored store must have a goal")
	}
	if len(full.Conditions) != 1 || full.Conditions[0].Desc != "spec changed" {
		t.Fatalf("restored conditions = %+v, want the file condition", full.Conditions)
	}
	if full.Conditions[0].Predicate.Target != "/work/spec.md" || full.Conditions[0].Baseline != "v1" {
		t.Fatalf("restored condition = %+v, want predicate + baseline", full.Conditions[0])
	}
	if full.Conditions[0].Predicate.Timeout != 5*time.Minute {
		t.Fatalf("restored condition timeout = %v, want 5m (persisted TimeoutNanos must restore)", full.Conditions[0].Predicate.Timeout)
	}
	if len(full.PendingWake) != 1 || full.PendingWake[0].Kind != goal.WaitUntilTime {
		t.Fatalf("restored pendingWake = %+v, want the structural kind", full.PendingWake)
	}
	if len(full.Waits) != 0 {
		t.Fatalf("restored waits = %d, want 0 (claimed lease left the registry)", len(full.Waits))
	}
}

// goalPersistCondSubstrate is the persist-test file substrate.
type goalPersistCondSubstrate struct {
	files map[string]string
}

func (f *goalPersistCondSubstrate) LookupJob(id string) (bool, bool, string, bool) {
	return false, false, "", false
}

func (f *goalPersistCondSubstrate) LookupDelegate(id string) (bool, bool, string, bool) {
	return false, false, "", false
}

func (f *goalPersistCondSubstrate) StatFile(path string) (string, bool) {
	b, ok := f.files[path]
	return b, ok
}

func (f *goalPersistCondSubstrate) LookupApproval(contentKey, generation string) bool {
	return false
}

func (f *goalPersistCondSubstrate) LookupChild(id string) bool { return false }
