package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// ---------------------------------------------------------------------------
// attentionResolutionPlansLocked
// ---------------------------------------------------------------------------

func TestAttentionResolutionPlansLocked_NilAggregate(t *testing.T) {
	c := &delegateTreeController{}
	plans := c.attentionResolutionPlansLocked(delegateLease{delegateID: "dlg_1"}, nil, nil)
	if plans != nil {
		t.Fatalf("expected nil for nil aggregate, got %#v", plans)
	}
}

func TestAttentionResolutionPlansLocked_PhaseStopping(t *testing.T) {
	c := &delegateTreeController{}
	agg := &delegatestore.Aggregate{Phase: delegatestore.PhaseStopping}
	lease := delegateLease{delegateID: "dlg_1"}
	plans := c.attentionResolutionPlansLocked(lease, agg, &delegateLiveState{})
	if plans != nil {
		t.Fatalf("expected nil for stopping phase, got %#v", plans)
	}
}

func TestAttentionResolutionPlansLocked_NotAttentionTrigger(t *testing.T) {
	c := &delegateTreeController{}
	agg := &delegatestore.Aggregate{Phase: delegatestore.PhaseRunning, Trigger: delegatestore.TriggerInitial}
	lease := delegateLease{delegateID: "dlg_1"}
	plans := c.attentionResolutionPlansLocked(lease, agg, &delegateLiveState{attentionIDs: []string{"att-1"}})
	if plans != nil {
		t.Fatalf("expected nil for non-attention trigger, got %#v", plans)
	}
}

func TestAttentionResolutionPlansLocked_NoAttentionIDs(t *testing.T) {
	c := &delegateTreeController{}
	agg := &delegatestore.Aggregate{Phase: delegatestore.PhaseRunning, Trigger: delegatestore.TriggerAttention}
	lease := delegateLease{delegateID: "dlg_1"}
	live := &delegateLiveState{}
	plans := c.attentionResolutionPlansLocked(lease, agg, live)
	if plans != nil {
		t.Fatalf("expected nil for no attention IDs, got %#v", plans)
	}
}

func TestAttentionResolutionPlansLocked_WithIDs(t *testing.T) {
	c := &delegateTreeController{}
	agg := &delegatestore.Aggregate{
		Phase:   delegatestore.PhaseRunning,
		Trigger: delegatestore.TriggerAttention,
		Descriptor: delegatestore.Descriptor{
			TranscriptRef: "local:child-sess",
		},
	}
	lease := delegateLease{delegateID: "dlg_1"}
	live := &delegateLiveState{attentionIDs: []string{"att-1", "att-2"}}
	plans := c.attentionResolutionPlansLocked(lease, agg, live)
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}
	if plans[0].attentionID != "att-1" || plans[1].attentionID != "att-2" {
		t.Fatalf("unexpected plan order: %#v", plans)
	}
	if plans[0].transcriptRef != "local:child-sess" {
		t.Fatalf("unexpected transcript ref: %q", plans[0].transcriptRef)
	}
	if plans[0].disposition != delegateAttentionConsumed {
		t.Fatalf("expected consumed disposition, got %v", plans[0].disposition)
	}
}

// ---------------------------------------------------------------------------
// finalizationReadyLocked
// ---------------------------------------------------------------------------

func TestFinalizationReadyLocked_NilClaim(t *testing.T) {
	c := &delegateTreeController{}
	if err := c.finalizationReadyLocked(nil); err != errDelegateStaleLease {
		t.Fatalf("expected stale lease for nil claim, got %v", err)
	}
}

func TestFinalizationReadyLocked_ClaimNotInMap(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{},
	}
	claim := &delegateSettlementClaim{token: 1}
	if err := c.finalizationReadyLocked(claim); err != errDelegateStaleLease {
		t.Fatalf("expected stale lease, got %v", err)
	}
}

func TestFinalizationReadyLocked_NotReady(t *testing.T) {
	ready := make(chan struct{})
	claim := &delegateSettlementClaim{token: 1, lease: delegateLease{delegateID: "dlg_1"}, ready: ready}
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: claim},
	}
	if err := c.finalizationReadyLocked(claim); err != errDelegateTargetBusy {
		t.Fatalf("expected target busy, got %v", err)
	}
}

func TestFinalizationReadyLocked_NoLiveBinding(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	claim := &delegateSettlementClaim{token: 1, lease: delegateLease{delegateID: "dlg_1"}, ready: ready}
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: claim},
	}
	if err := c.finalizationReadyLocked(claim); err != errDelegateStaleLease {
		t.Fatalf("expected stale lease for no live binding, got %v", err)
	}
}

func TestFinalizationReadyLocked_LeaseMismatch(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	claim := &delegateSettlementClaim{token: 1, lease: delegateLease{delegateID: "dlg_1", generation: 1}, ready: ready}
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: claim},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: delegateLease{delegateID: "dlg_1", generation: 2}}},
		},
	}
	if err := c.finalizationReadyLocked(claim); err != errDelegateStaleLease {
		t.Fatalf("expected stale lease for lease mismatch, got %v", err)
	}
}

func TestFinalizationReadyLocked_QuietClaimActive(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	claim := &delegateSettlementClaim{token: 1, lease: lease, ready: ready}
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: claim},
		live: map[string]*delegateLiveState{
			"dlg_1": {
				binding:    &delegateRuntimeBinding{lease: lease},
				quietClaim: &delegateQuietAttentionClaim{},
			},
		},
	}
	if err := c.finalizationReadyLocked(claim); err != errDelegateTargetBusy {
		t.Fatalf("expected target busy for quiet claim, got %v", err)
	}
}

func TestFinalizationReadyLocked_Ready(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	claim := &delegateSettlementClaim{token: 1, lease: lease, ready: ready}
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: claim},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: lease}},
		},
	}
	if err := c.finalizationReadyLocked(claim); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// finalizationReadyForLeaseLocked
// ---------------------------------------------------------------------------

func TestFinalizationReadyForLeaseLocked_QuietClaimActive(t *testing.T) {
	c := &delegateTreeController{}
	live := &delegateLiveState{quietClaim: &delegateQuietAttentionClaim{}}
	if err := c.finalizationReadyForLeaseLocked(delegateLease{delegateID: "dlg_1"}, live); err != errDelegateTargetBusy {
		t.Fatalf("expected target busy, got %v", err)
	}
}

func TestFinalizationReadyForLeaseLocked_NoClaim(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{},
	}
	if err := c.finalizationReadyForLeaseLocked(delegateLease{delegateID: "dlg_1"}, &delegateLiveState{}); err != nil {
		t.Fatalf("expected nil for no claim, got %v", err)
	}
}

func TestFinalizationReadyForLeaseLocked_WithClaim(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	claim := &delegateSettlementClaim{token: 1, lease: lease, ready: ready}
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: claim},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{lease: lease}},
		},
	}
	if err := c.finalizationReadyForLeaseLocked(lease, c.live["dlg_1"]); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
