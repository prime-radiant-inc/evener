package agent

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
)

// Round-18 fix wave (roborev review on rev 3d3835a): wake-turn budget
// accounting. HIGH: a sibling-live wake drive ran a real model turn but folded
// through RecordContinuation, which skips while waiting — a free turn (spec
// §5). MEDIUM: the wake fold can land exactly on MaxContinuations, but the
// wake branch rendered unconditionally and the wake tail re-armed without a
// bounds check — an extra turn past the cap.

// TestFixWave18_SiblingLiveWakeDriveFolds pins the HIGH: two live waits, one
// fires (ClaimFire leaves the sibling live, goal stays waiting), and the wake
// drive still accrues exactly one continuation + iteration.
func TestFixWave18_SiblingLiveWakeDriveFolds(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("timer goal with sibling", clk.Now())
	w1, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "t1", Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: wait 1 registration should succeed")
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "t2", Timeout: 2 * time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: wait 2 registration should succeed")
	}
	if _, ok := store.ClaimFire(w1.Lease.WaitID, "timer fired", clk.Now()); !ok {
		t.Fatal("precondition: ClaimFire should consume the first lease")
	}
	full, _ := store.GoalSnapshot()
	if full.Status != goal.StatusWaiting || len(full.Waits) != 1 || len(full.PendingWake) != 1 {
		t.Fatalf("precondition: sibling live keeps the goal waiting with the backlog standing: %+v", full)
	}

	outcome := ledgerGateOutcome("ship fp", "ok", "h1", "digest-1", true)
	prompt, cont := sess.armGoalContinuationWithOutcome(true, true, outcome)
	if !cont || prompt == "" {
		t.Fatalf("sibling-live wake gate = (%q, %v), want the wake drive", prompt, cont)
	}
	if !strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("wake prompt missing trailer %q\nprompt:\n%s", goalWaitWakeTrailerPrefix, prompt)
	}
	snap, _ := store.Snapshot()
	if snap.Iterations != 1 {
		t.Fatalf("sibling-live wake drive must fold: snapshot = %+v, want Iterations=1", snap)
	}
	after, _ := store.GoalSnapshot()
	if after.Budgets.UsedContinuations != 1 {
		t.Fatalf("sibling-live wake drive must burn one continuation: %+v", after.Budgets)
	}

	// The follow-up gate drains the delivered batch without re-folding, then
	// parks on the still-live sibling (rule 4) — no free re-arm, no re-fold
	// (spec §7: no double-turn accounting).
	prompt, cont = sess.armGoalContinuationWithOutcome(false, true, outcome)
	if cont || prompt != "" {
		t.Fatalf("post-wake gate = (%q, %v), want a park on the live sibling", prompt, cont)
	}
	tail, _ := store.GoalSnapshot()
	if len(tail.PendingWake) != 0 {
		t.Fatalf("pendingWake after the post-wake gate = %+v, want drained", tail.PendingWake)
	}
	if tail.Status != goal.StatusWaiting || len(tail.Waits) != 1 {
		t.Fatalf("goal must stay waiting on the live sibling: %+v", tail)
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 1 {
		t.Fatalf("post-wake gate re-folded: snapshot = %+v, want still Iterations=1", snap)
	}
}

// TestFixWave18_WakeDriveLandingOnCapBlocks pins MEDIUM site (a): the wake
// fold lands exactly on MaxContinuations, so the gate must block with the
// budget-exhausted terminal path instead of rendering the wake prompt.
func TestFixWave18_WakeDriveLandingOnCapBlocks(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("cap the wake", clk.Now())
	w1, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "t1", Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: wait 1 registration should succeed")
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "t2", Timeout: 2 * time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: wait 2 registration should succeed")
	}
	if _, ok := store.ClaimFire(w1.Lease.WaitID, "timer fired", clk.Now()); !ok {
		t.Fatal("precondition: ClaimFire should consume the first lease")
	}
	// Shrink the cap to Used+1: the committed wake fold lands exactly on it.
	full, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("precondition: goal must stand")
	}
	persisted, ok := goal.PersistedFromSnapshot(full)
	if !ok {
		t.Fatal("precondition: PersistedFromSnapshot should convert the live snapshot")
	}
	persisted.Budgets.MaxContinuations = persisted.Budgets.UsedContinuations + 1
	store.RestoreSnapshot(persisted)

	progress := ledgerGateOutcome("ship fp", "ok", "h1", "digest-1", true)
	prompt, cont := sess.armGoalContinuationWithOutcome(true, true, progress)
	if cont || prompt != "" {
		t.Fatalf("cap-hitting wake gate = (%q, %v), want a block (no rendered wake prompt)", prompt, cont)
	}
	after, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("goal must still stand after the block")
	}
	if after.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want blocked", after.Status)
	}
	if after.StopReason != goal.VerdictBudgetExhausted {
		t.Fatalf("stop = %q, want %q", after.StopReason, goal.VerdictBudgetExhausted)
	}
	if len(after.Waits) != 0 {
		t.Fatalf("terminal transition must clear waits: %+v", after.Waits)
	}
	if got := countTask7SteeringNotes(sess, "budget exhausted"); got != 1 {
		t.Fatalf("budget-exhausted steering notes = %d, want exactly 1", got)
	}
}

// TestFixWave18_WakeTailAtCapBlocks pins MEDIUM site (b): the wake drive
// folds under the cap, but the cap is spent before the tail gate runs (a fresh
// shrink models the mid-turn crossing), so the tail must block instead of
// re-arming the plain objective.
func TestFixWave18_WakeTailAtCapBlocks(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tail at cap", clk.Now())
	w1, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "t1", Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: wait 1 registration should succeed")
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "t2", Timeout: 2 * time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: wait 2 registration should succeed")
	}
	if _, ok := store.ClaimFire(w1.Lease.WaitID, "timer fired", clk.Now()); !ok {
		t.Fatal("precondition: ClaimFire should consume the first lease")
	}
	progress := ledgerGateOutcome("ship fp", "ok", "h1", "digest-1", true)
	prompt, cont := sess.armGoalContinuationWithOutcome(true, true, progress)
	if !cont || prompt == "" {
		t.Fatalf("precondition: wake gate = (%q, %v), want the wake drive", prompt, cont)
	}
	// Spend the cap between the drive and its tail: the tail re-arm would hand
	// the model a free turn past the cap.
	full, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("precondition: goal must stand after the wake drive")
	}
	persisted, ok := goal.PersistedFromSnapshot(full)
	if !ok {
		t.Fatal("precondition: PersistedFromSnapshot should convert the live snapshot")
	}
	persisted.Budgets.MaxContinuations = persisted.Budgets.UsedContinuations
	store.RestoreSnapshot(persisted)

	prompt, cont = sess.armGoalContinuationWithOutcome(false, true, progress)
	if cont || prompt != "" {
		t.Fatalf("at-cap wake tail = (%q, %v), want a block (no re-armed prompt)", prompt, cont)
	}
	after, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("goal must still stand after the block")
	}
	if after.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want blocked", after.Status)
	}
	if after.StopReason != goal.VerdictBudgetExhausted {
		t.Fatalf("stop = %q, want %q", after.StopReason, goal.VerdictBudgetExhausted)
	}
	if got := countTask7SteeringNotes(sess, "budget exhausted"); got != 1 {
		t.Fatalf("budget-exhausted steering notes = %d, want exactly 1", got)
	}
}
