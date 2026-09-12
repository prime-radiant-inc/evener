package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
)

// Round-19 fix wave (roborev review on rev bd2fda6). MEDIUM
// approval-cancel misreported: a missing live ask ALWAYS fired "approval
// answered", but askPending clears on interrupt and on every accepted turn
// entry, so a cleared-but-unanswered ask is indistinguishable from an answered
// one at the substrate. MEDIUM terminal backlog carried into retarget:
// SetTerminal preserved PendingWake "for the gate to drain", but the gate
// short-circuits terminals before draining, so the backlog rode into the next
// Set as Superseded - one no-op continuation with stale context on the new
// objective.

// TestFixWave19_ApprovalClearedAskFiresNeutral pins the approval half at the
// gate: an until_approval wait whose ask disappears (cancelled, never
// answered) still fires for re-validation (no strand), but the wake prompt
// must NOT claim an answer - the reply turn's content carries the verdict.
func TestFixWave19_ApprovalClearedAskFiresNeutral(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sub := &fixStubSubstrate{approvals: map[string]bool{"ship it?\x00gen1": true}}
	store := sess.getOrCreateGoalStore()
	store.Set("approval gate", clk.Now())
	store.SetSubstrate(sub)
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilApproval, Target: "ship it?", AskGeneration: "gen1", Timeout: time.Hour}, clk.Now())
	if !ok {
		t.Fatalf("precondition: live ask must park: %q", store.LastRejectReason())
	}
	// Live ask still matching: the gate parks, no wake drives.
	if prompt, cont := sess.armGoalContinuation(false, true); cont || prompt != "" {
		t.Fatalf("live-ask gate = (%q, %v), want a park", prompt, cont)
	}
	// The ask is cancelled, never answered: the substrate reads identically to
	// an answer (no live ask), so the gate fires for re-validation - but the
	// wake prompt must not assert an answer.
	delete(sub.approvals, "ship it?\x00gen1")
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || prompt == "" {
		t.Fatalf("cleared-ask gate = (%q, %v), want the re-validation wake drive", prompt, cont)
	}
	if strings.Contains(prompt, "answered") {
		t.Fatalf("wake prompt must not claim an answer the predicate cannot verify:\n%s", prompt)
	}
	if !strings.Contains(prompt, w.Lease.Label) {
		t.Fatalf("wake prompt must carry the lease label %q:\n%s", w.Lease.Label, prompt)
	}
}

// TestFixWave19_TerminalBacklogNotCarriedIntoRetarget pins the terminal half
// end to end: a wake continuation calls update_goal complete with the backlog
// still standing, PendingWake drains at terminalization, and the retargeted
// objective starts clean - the first gate drives/folds normally with no stale
// superseded trigger.
func TestFixWave19_TerminalBacklogNotCarriedIntoRetarget(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("old objective", clk.Now())
	_, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	// A wake continuation (as update_goal complete would observe after the
	// wake turn drove): claim the expired lease into the backlog directly,
	// then terminalize with the backlog still standing.
	full, _ := store.GoalSnapshot()
	var live []goal.Wait
	for _, lw := range full.Waits {
		if lw.Live() {
			live = append(live, lw)
		}
	}
	if len(live) != 1 {
		t.Fatalf("precondition: want one live lease, got %+v", full.Waits)
	}
	if _, ok := store.ClaimFire(live[0].Lease.WaitID, "wait expired: label", clk.Now()); !ok {
		t.Fatal("precondition: ClaimFire should consume the lease into the backlog")
	}
	if full, _ := store.GoalSnapshot(); len(full.PendingWake) == 0 {
		t.Fatalf("precondition: backlog must stand before terminalization: %+v", full)
	}
	if !store.SetTerminal(goal.StatusComplete, "", clk.Now()) {
		t.Fatal("precondition: SetTerminal should complete the wake continuation's goal")
	}
	if full, _ := store.GoalSnapshot(); len(full.PendingWake) != 0 {
		t.Fatalf("terminal goal must carry no backlog: %+v", full.PendingWake)
	}
	// Retarget onto the terminal: no Superseded carry, so the first gate on
	// the new objective drives the plain objective with a normal fold - never
	// a stale-trigger no-op.
	if _, err := sess.SetGoal(context.Background(), "new objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if full, _ := store.GoalSnapshot(); len(full.PendingWake) != 0 {
		t.Fatalf("retarget after terminal must carry no backlog: %+v", full.PendingWake)
	}
	outcome := ledgerGateOutcome("ship fp", "ok", "h1", "digest-1", true)
	prompt, cont := sess.armGoalContinuationWithOutcome(true, true, outcome)
	if !cont || !strings.Contains(prompt, "new objective") {
		t.Fatalf("first retarget gate = (%q, %v), want the new-objective drive", prompt, cont)
	}
	if strings.Contains(prompt, "(superseded)") {
		t.Fatalf("retarget drive must not carry a stale superseded trigger:\n%s", prompt)
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 1 {
		t.Fatalf("retarget drive must fold normally: snapshot = %+v, want Iterations=1", snap)
	}
}
