package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// Regression test for the live attention-wake leak observed in production: a
// live controller (not a restore/bootstrap) with attention armed for a
// delegate that is then deliberately stopped (subtree stop settling
// stopped_by_parent) must discard the bound attention at settlement, exactly
// the way bootstrap discards restored stop-bound attention. Before the fix,
// an attention wake noted after the stop drain's final evidence collection
// but before the stop-completion append survived the completed stop: the
// in-memory wake ID leaked, hasPendingDelegateAttention stayed true forever,
// and the attention drive retried the stop-gated delegate every cycle.
//
// The race is modelled deterministically: the reconcile evidence is the
// pre-arm snapshot (empty attention) while the controller already holds the
// wake ID, which is exactly the window between collectDelegateReconcileEvidence
// and Reconcile's completion append. The transcript open is durable before
// the wake is noted, mirroring the arm path's append-then-note order.
func TestDelegateAttention_LiveStopCompletionDiscardsLateBoundWake(t *testing.T) {
	const (
		delegateID     = "dlg_target"
		childSessionID = "child-dlg_target"
		attentionID    = "attention-armed-during-stop-drain"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, delegateID, "")
	writeDelegateAttentionTranscript(t, transcriptPath(c.stateDir, childSessionID), childSessionID, attentionID)

	result, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	// The cancelled run loop settles its generation through the controller's
	// own finish path, committing OutcomeStopped/stopped_by_parent.
	lease := delegateLease{delegateID: delegateID, generation: 1}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}

	// A shell/watch notification arms attention in the gap between the drain's
	// last evidence collection and its stop-completion append.
	c.noteDelegateAttention(delegateID, attentionID)
	evidence := emptyDelegateReconcileEvidence(c)

	// Drive the stop reconcile to completion the way drainStop does, executing
	// any attention cleanup the controller asks for.
	for range 4 {
		plans, err := c.Reconcile(evidence)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		for _, plan := range plans.attention {
			if plan.disposition != delegateAttentionDiscarded {
				t.Fatalf("stop-bound attention disposition = %q, want %q", plan.disposition, delegateAttentionDiscarded)
			}
			if err := c.executeDelegateAttentionCleanup(plan); err != nil {
				t.Fatalf("executeDelegateAttentionCleanup: %v", err)
			}
		}
		if delegateStopDone(c.stop) {
			break
		}
		evidence = emptyDelegateReconcileEvidence(c)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop did not complete")
	}

	aggregate := c.durable[delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.PendingStopSeq != 0 ||
		aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped ||
		aggregate.LatestOutcome.Reason != "stopped_by_parent" {
		t.Fatalf("settled stop aggregate = %#v", aggregate)
	}
	if got := len(c.attentionWakeIDs[delegateID]); got != 0 {
		t.Fatalf("stop-gated delegate retained %d attention wake IDs", got)
	}
	if c.hasPendingDelegateAttention() {
		t.Fatal("hasPendingDelegateAttention is true for a deliberately stopped delegate")
	}
	if nextID, _, pending := c.nextIdleDelegateAttention(); pending {
		t.Fatalf("attention drive would resurrect stop-gated delegate %q", nextID)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, childSessionID), childSessionID)
	if err != nil {
		t.Fatalf("read stopped child attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("stop-bound attention disposition = %q, want %q", got, delegateAttentionDiscarded)
	}
}

// Companion end-to-end check: when the bound attention is already visible to
// the drain's evidence collection, the live stop drain discards it before
// completing the stop.
func TestDelegateAttention_LiveStopSettlementDiscardsBoundAttention(t *testing.T) {
	const (
		delegateID     = "dlg_target"
		childSessionID = "child-dlg_target"
		attentionID    = "attention-armed-before-live-stop"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, delegateID, "")
	writeDelegateAttentionTranscript(t, transcriptPath(c.stateDir, childSessionID), childSessionID, attentionID)
	c.noteDelegateAttention(delegateID, attentionID)
	if !c.hasPendingDelegateAttention() {
		t.Fatal("armed attention is not pending before the stop")
	}

	result, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	lease := delegateLease{delegateID: delegateID, generation: 1}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}

	c.mu.Lock()
	stop := c.stop
	c.mu.Unlock()
	if stop == nil {
		t.Fatal("stop state missing after admission")
	}
	if err := c.drainStop(context.Background(), stop, nil); err != nil {
		t.Fatalf("drainStop: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop did not complete after drain")
	}

	aggregate := c.durable[delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.PendingStopSeq != 0 ||
		aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped ||
		aggregate.LatestOutcome.Reason != "stopped_by_parent" {
		t.Fatalf("settled stop aggregate = %#v", aggregate)
	}

	if got := len(c.attentionWakeIDs[delegateID]); got != 0 {
		t.Fatalf("stop-gated delegate retained %d attention wake IDs", got)
	}
	if c.hasPendingDelegateAttention() {
		t.Fatal("hasPendingDelegateAttention is true for a deliberately stopped delegate")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, childSessionID), childSessionID)
	if err != nil {
		t.Fatalf("read stopped child attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("live stop attention disposition = %q, want %q", got, delegateAttentionDiscarded)
	}
}
