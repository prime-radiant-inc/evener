package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
)

// TestGoalContinuationCapHitBlocksInsteadOfRendering is the failing-first pin
// for the round-14 MEDIUM (continuation cap hardening): the plain-drive fold
// commits the cap-hitting turn, so the gate must route to the budget-exhausted
// terminal path instead of rendering another prompt. Setup: MaxContinuations
// = Used+1 with a progressing outcome (no stall trip — the cap alone must
// stop the drive). Before the fix the gate renders a prompt (cont=true);
// after, it blocks ("", false) with the goal blocked / "budget exhausted".
func TestGoalContinuationCapHitBlocksInsteadOfRendering(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("cap the drive", clk.Now())
	// Progressing outcome: Mutated=true keeps the stall bound quiet so the
	// cap is the only stop signal.
	progress := ledgerGateOutcome("ship fp", "ok", "h1", "digest-1", true)
	prompt, cont := sess.armGoalContinuationWithOutcome(true, true, progress)
	if !cont || prompt == "" {
		t.Fatalf("precondition: first gate = (%q, %v), want a drive", prompt, cont)
	}
	full, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("precondition: goal must stand")
	}
	// Shrink the cap to Used+1: the next committed fold lands exactly on it.
	persisted, ok := goal.PersistedFromSnapshot(full)
	if !ok {
		t.Fatal("precondition: PersistedFromSnapshot should convert the live snapshot")
	}
	persisted.Budgets.MaxContinuations = persisted.Budgets.UsedContinuations + 1
	store.RestoreSnapshot(persisted)

	progress2 := ledgerGateOutcome("ship fp", "ok", "h2", "digest-2", true)
	prompt, cont = sess.armGoalContinuationWithOutcome(true, true, progress2)
	if cont || prompt != "" {
		t.Fatalf("cap-hitting gate = (%q, %v), want a block (no rendered prompt)", prompt, cont)
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
