package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// ---------------------------------------------------------------------------
// delegateAttentionResolution constants
// ---------------------------------------------------------------------------

func TestDelegateAttentionResolutionConstants(t *testing.T) {
	if delegateAttentionConsumed != "consumed" {
		t.Fatalf("delegateAttentionConsumed = %q", delegateAttentionConsumed)
	}
	if delegateAttentionDiscarded != "discarded" {
		t.Fatalf("delegateAttentionDiscarded = %q", delegateAttentionDiscarded)
	}
}

// ---------------------------------------------------------------------------
// delegateRunStartIndex
// ---------------------------------------------------------------------------

func TestDelegateRunStartIndex(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		index := delegateRunStartIndex(nil)
		if len(index) != 0 {
			t.Fatalf("expected 0 entries")
		}
	})
	t.Run("with RunStarted events", func(t *testing.T) {
		events := []delegatestore.Event{
			{DelegateID: "dlg_1", RunStarted: &delegatestore.RunStarted{Generation: 1, Trigger: delegatestore.TriggerAttention}},
			{DelegateID: "dlg_2", RunStarted: &delegatestore.RunStarted{Generation: 3, Trigger: delegatestore.TriggerOwnerInput}},
			{DelegateID: "dlg_1", RunStarted: nil}, // skipped
		}
		index := delegateRunStartIndex(events)
		if len(index) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(index))
		}
		trigger, ok := index[delegateLease{delegateID: "dlg_1", generation: 1}]
		if !ok || trigger != delegatestore.TriggerAttention {
			t.Fatalf("dlg_1 gen 1 = %v ok=%v", trigger, ok)
		}
		trigger2, ok2 := index[delegateLease{delegateID: "dlg_2", generation: 3}]
		if !ok2 || trigger2 != delegatestore.TriggerOwnerInput {
			t.Fatalf("dlg_2 gen 3 = %v ok=%v", trigger2, ok2)
		}
	})
	t.Run("no RunStarted events", func(t *testing.T) {
		events := []delegatestore.Event{
			{DelegateID: "dlg_1"},
			{DelegateID: "dlg_2", RunStarted: nil},
		}
		index := delegateRunStartIndex(events)
		if len(index) != 0 {
			t.Fatalf("expected 0 entries, got %d", len(index))
		}
	})
}

// ---------------------------------------------------------------------------
// delegateOpenRunOrder
// ---------------------------------------------------------------------------

func TestDelegateOpenRunOrder(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		order := delegateOpenRunOrder(nil, delegatestore.State{})
		if len(order) != 0 {
			t.Fatalf("expected 0 entries")
		}
	})
	t.Run("with open runs", func(t *testing.T) {
		events := []delegatestore.Event{
			{Seq: 1, DelegateID: "dlg_1", RunStarted: &delegatestore.RunStarted{Generation: 1}},
			{Seq: 2, DelegateID: "dlg_2", RunStarted: &delegatestore.RunStarted{Generation: 1}},
		}
		state := delegatestore.State{
			"dlg_1": &delegatestore.Aggregate{CurrentRunOpen: true, Generation: 1},
			"dlg_2": &delegatestore.Aggregate{CurrentRunOpen: true, Generation: 1},
		}
		order := delegateOpenRunOrder(events, state)
		if len(order) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(order))
		}
		if order[0].delegateID != "dlg_1" || order[0].generation != 1 {
			t.Fatalf("order[0] = %+v", order[0])
		}
	})
	t.Run("skips closed runs", func(t *testing.T) {
		events := []delegatestore.Event{
			{Seq: 1, DelegateID: "dlg_1", RunStarted: &delegatestore.RunStarted{Generation: 1}},
		}
		state := delegatestore.State{
			"dlg_1": &delegatestore.Aggregate{CurrentRunOpen: false, Generation: 1},
		}
		order := delegateOpenRunOrder(events, state)
		if len(order) != 0 {
			t.Fatalf("expected 0 entries for closed run, got %d", len(order))
		}
	})
	t.Run("skips nil aggregate", func(t *testing.T) {
		events := []delegatestore.Event{
			{Seq: 1, DelegateID: "dlg_1", RunStarted: &delegatestore.RunStarted{Generation: 1}},
		}
		state := delegatestore.State{
			"dlg_1": nil,
		}
		order := delegateOpenRunOrder(events, state)
		if len(order) != 0 {
			t.Fatalf("expected 0 entries for nil aggregate, got %d", len(order))
		}
	})
	t.Run("skips mismatched generation", func(t *testing.T) {
		events := []delegatestore.Event{
			{Seq: 1, DelegateID: "dlg_1", RunStarted: &delegatestore.RunStarted{Generation: 1}},
		}
		state := delegatestore.State{
			"dlg_1": &delegatestore.Aggregate{CurrentRunOpen: true, Generation: 2},
		}
		order := delegateOpenRunOrder(events, state)
		if len(order) != 0 {
			t.Fatalf("expected 0 entries for mismatched generation, got %d", len(order))
		}
	})
}

// ---------------------------------------------------------------------------
// nearestReachableAttentionAncestorLocked
// ---------------------------------------------------------------------------

func TestNearestReachableAttentionAncestorLocked(t *testing.T) {
	t.Run("empty parentID", func(t *testing.T) {
		if id := nearestReachableAttentionAncestorLocked(delegatestore.State{}, ""); id != "" {
			t.Fatalf("expected empty, got %q", id)
		}
	})
	t.Run("direct parent reachable", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_parent": &delegatestore.Aggregate{
				Resumable: true,
				Phase:     delegatestore.PhaseIdle,
			},
		}
		id := nearestReachableAttentionAncestorLocked(state, "dlg_parent")
		if id != "dlg_parent" {
			t.Fatalf("expected dlg_parent, got %q", id)
		}
	})
	t.Run("parent not resumable", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_parent": &delegatestore.Aggregate{
				Resumable: false,
				Phase:     delegatestore.PhaseIdle,
			},
		}
		id := nearestReachableAttentionAncestorLocked(state, "dlg_parent")
		if id != "" {
			t.Fatalf("expected empty for non-resumable parent, got %q", id)
		}
	})
	t.Run("parent not idle", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_parent": &delegatestore.Aggregate{
				Resumable: true,
				Phase:     delegatestore.PhaseRunning,
			},
		}
		id := nearestReachableAttentionAncestorLocked(state, "dlg_parent")
		if id != "" {
			t.Fatalf("expected empty for non-idle parent, got %q", id)
		}
	})
	t.Run("parent has pending stop", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_parent": &delegatestore.Aggregate{
				Resumable:      true,
				Phase:          delegatestore.PhaseIdle,
				PendingStopSeq: 5,
			},
		}
		id := nearestReachableAttentionAncestorLocked(state, "dlg_parent")
		if id != "" {
			t.Fatalf("expected empty for parent with pending stop, got %q", id)
		}
	})
	t.Run("nil parent aggregate", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_parent": nil,
		}
		id := nearestReachableAttentionAncestorLocked(state, "dlg_parent")
		if id != "" {
			t.Fatalf("expected empty for nil parent, got %q", id)
		}
	})
	t.Run("grandparent reachable", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_parent": &delegatestore.Aggregate{
				Resumable:  false,
				Phase:      delegatestore.PhaseClosed,
				Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_grand"},
			},
			"dlg_grand": &delegatestore.Aggregate{
				Resumable: true,
				Phase:     delegatestore.PhaseIdle,
			},
		}
		id := nearestReachableAttentionAncestorLocked(state, "dlg_parent")
		if id != "dlg_grand" {
			t.Fatalf("expected dlg_grand, got %q", id)
		}
	})
	t.Run("chain all unreachable", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_a": &delegatestore.Aggregate{
				Resumable:  false,
				Phase:      delegatestore.PhaseClosed,
				Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_b"},
			},
			"dlg_b": &delegatestore.Aggregate{
				Resumable:  false,
				Phase:      delegatestore.PhaseClosed,
				Descriptor: delegatestore.Descriptor{ParentDelegateID: ""},
			},
		}
		id := nearestReachableAttentionAncestorLocked(state, "dlg_a")
		if id != "" {
			t.Fatalf("expected empty for all-unreachable chain, got %q", id)
		}
	})
}

// ---------------------------------------------------------------------------
// delegateReconcileEvidenceMatchesState
// ---------------------------------------------------------------------------

func TestDelegateReconcileEvidenceMatchesState(t *testing.T) {
	t.Run("matching", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_1": &delegatestore.Aggregate{},
			"dlg_2": &delegatestore.Aggregate{},
		}
		evidence := delegateReconcileEvidence{
			shells:    map[string]shellRuntimeLossEvidence{"dlg_1": {}, "dlg_2": {}},
			attention: map[string][]string{"dlg_1": {}, "dlg_2": {}},
		}
		if !delegateReconcileEvidenceMatchesState(evidence, state) {
			t.Fatalf("expected match")
		}
	})
	t.Run("shell count mismatch", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_1": &delegatestore.Aggregate{},
		}
		evidence := delegateReconcileEvidence{
			shells:    map[string]shellRuntimeLossEvidence{"dlg_1": {}, "dlg_2": {}},
			attention: map[string][]string{"dlg_1": {}},
		}
		if delegateReconcileEvidenceMatchesState(evidence, state) {
			t.Fatalf("expected no match for shell count mismatch")
		}
	})
	t.Run("attention count mismatch", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_1": &delegatestore.Aggregate{},
		}
		evidence := delegateReconcileEvidence{
			shells:    map[string]shellRuntimeLossEvidence{"dlg_1": {}},
			attention: map[string][]string{"dlg_1": {}, "dlg_2": {}},
		}
		if delegateReconcileEvidenceMatchesState(evidence, state) {
			t.Fatalf("expected no match for attention count mismatch")
		}
	})
	t.Run("nil aggregate in state", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_1": nil,
		}
		evidence := delegateReconcileEvidence{
			shells:    map[string]shellRuntimeLossEvidence{"dlg_1": {}},
			attention: map[string][]string{"dlg_1": {}},
		}
		if delegateReconcileEvidenceMatchesState(evidence, state) {
			t.Fatalf("expected no match for nil aggregate")
		}
	})
	t.Run("missing shell evidence", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_1": &delegatestore.Aggregate{},
			"dlg_2": &delegatestore.Aggregate{},
		}
		evidence := delegateReconcileEvidence{
			shells:    map[string]shellRuntimeLossEvidence{"dlg_1": {}},
			attention: map[string][]string{"dlg_1": {}, "dlg_2": {}},
		}
		if delegateReconcileEvidenceMatchesState(evidence, state) {
			t.Fatalf("expected no match for missing shell evidence")
		}
	})
	t.Run("missing attention evidence", func(t *testing.T) {
		state := delegatestore.State{
			"dlg_1": &delegatestore.Aggregate{},
			"dlg_2": &delegatestore.Aggregate{},
		}
		evidence := delegateReconcileEvidence{
			shells:    map[string]shellRuntimeLossEvidence{"dlg_1": {}, "dlg_2": {}},
			attention: map[string][]string{"dlg_1": {}},
		}
		if delegateReconcileEvidenceMatchesState(evidence, state) {
			t.Fatalf("expected no match for missing attention evidence")
		}
	})
	t.Run("both empty", func(t *testing.T) {
		if !delegateReconcileEvidenceMatchesState(delegateReconcileEvidence{}, delegatestore.State{}) {
			t.Fatalf("expected match for both empty")
		}
	})
}

// ---------------------------------------------------------------------------
// takeOwedAttentionAdmission
// ---------------------------------------------------------------------------

func TestTakeOwedAttentionAdmissionNil(t *testing.T) {
	var c *delegateTreeController
	if c.takeOwedAttentionAdmission() {
		t.Fatalf("expected false for nil controller")
	}
}

func TestTakeOwedAttentionAdmissionFalse(t *testing.T) {
	c := &delegateTreeController{owedAdmission: false}
	if c.takeOwedAttentionAdmission() {
		t.Fatalf("expected false when owedAdmission is false")
	}
}

func TestTakeOwedAttentionAdmissionTrue(t *testing.T) {
	c := &delegateTreeController{owedAdmission: true}
	if !c.takeOwedAttentionAdmission() {
		t.Fatalf("expected true when owedAdmission is true")
	}
	// Should be consumed
	if c.owedAdmission {
		t.Fatalf("expected owedAdmission to be consumed (set to false)")
	}
}

// ---------------------------------------------------------------------------
// owedAttentionStartsFromTranscripts nil controller
// ---------------------------------------------------------------------------

func TestOwedAttentionStartsFromTranscriptsNil(t *testing.T) {
	var c *delegateTreeController
	_, err := c.owedAttentionStartsFromTranscripts()
	if err == nil || err.Error() != "delegate controller is nil" {
		t.Fatalf("expected nil controller error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// repairPermanentlyUnreachableDelegateAttention nil controller
// ---------------------------------------------------------------------------

func TestRepairPermanentlyUnreachableDelegateAttentionNil(t *testing.T) {
	var c *delegateTreeController
	err := repairPermanentlyUnreachableDelegateAttention(c)
	if err == nil || err.Error() != "delegate controller is nil" {
		t.Fatalf("expected nil controller error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// delegateAttentionCleanupPlan struct
// ---------------------------------------------------------------------------

func TestDelegateAttentionCleanupPlanStruct(t *testing.T) {
	plan := delegateAttentionCleanupPlan{
		requestSeq:      5,
		evidenceVersion: 10,
		delegateID:      "dlg_1",
		transcriptRef:   "local:abc",
		attentionID:     "att_1",
		disposition:     delegateAttentionDiscarded,
		stabilize:       true,
	}
	if plan.requestSeq != 5 || plan.delegateID != "dlg_1" || plan.attentionID != "att_1" {
		t.Fatalf("struct fields wrong: %+v", plan)
	}
	if plan.disposition != delegateAttentionDiscarded || !plan.stabilize {
		t.Fatalf("struct fields wrong: %+v", plan)
	}
}

// ---------------------------------------------------------------------------
// delegateShellRepairPlan struct
// ---------------------------------------------------------------------------

func TestDelegateShellRepairPlanStruct(t *testing.T) {
	plan := delegateShellRepairPlan{
		delegateID:          "dlg_1",
		storePath:           "/path/to/jobs.jsonl",
		runningJobIDs:       []string{"job_1", "job_2"},
		pendingNotification: []shellNotificationIdentity{{jobID: "job_1", terminalGeneration: "gen_1"}},
		suppressOwnerNotify: true,
	}
	if plan.delegateID != "dlg_1" || plan.storePath != "/path/to/jobs.jsonl" {
		t.Fatalf("struct fields wrong: %+v", plan)
	}
	if len(plan.runningJobIDs) != 2 || len(plan.pendingNotification) != 1 {
		t.Fatalf("slice fields wrong: %+v", plan)
	}
}

// ---------------------------------------------------------------------------
// shellNotificationIdentity struct
// ---------------------------------------------------------------------------

func TestShellNotificationIdentityStruct(t *testing.T) {
	id := shellNotificationIdentity{jobID: "job_1", terminalGeneration: "gen_2"}
	if id.jobID != "job_1" || id.terminalGeneration != "gen_2" {
		t.Fatalf("struct wrong: %+v", id)
	}
}

// ---------------------------------------------------------------------------
// shellRuntimeLossEvidence struct
// ---------------------------------------------------------------------------

func TestShellRuntimeLossEvidenceStruct(t *testing.T) {
	ev := shellRuntimeLossEvidence{
		runningJobIDs:       []string{"job_1"},
		pendingNotification: []shellNotificationIdentity{{jobID: "job_2"}},
	}
	if len(ev.runningJobIDs) != 1 || ev.runningJobIDs[0] != "job_1" {
		t.Fatalf("runningJobIDs wrong: %v", ev.runningJobIDs)
	}
	if len(ev.pendingNotification) != 1 || ev.pendingNotification[0].jobID != "job_2" {
		t.Fatalf("pendingNotification wrong: %v", ev.pendingNotification)
	}
}

// ---------------------------------------------------------------------------
// delegateReconcileRequirements struct
// ---------------------------------------------------------------------------

func TestDelegateReconcileRequirementsStruct(t *testing.T) {
	req := delegateReconcileRequirements{
		evidenceVersion:      5,
		shellStores:          map[string]string{"dlg_1": "/path/jobs.jsonl"},
		attentionTranscripts: map[string]string{"dlg_1": "local:abc"},
	}
	if req.evidenceVersion != 5 {
		t.Fatalf("evidenceVersion = %d", req.evidenceVersion)
	}
	if len(req.shellStores) != 1 || len(req.attentionTranscripts) != 1 {
		t.Fatalf("maps wrong: %+v", req)
	}
}

// ---------------------------------------------------------------------------
// delegateReconcileEvidence struct
// ---------------------------------------------------------------------------

func TestDelegateReconcileEvidenceStruct(t *testing.T) {
	ev := delegateReconcileEvidence{
		evidenceVersion: 3,
		shells:          map[string]shellRuntimeLossEvidence{"dlg_1": {}},
		attention:       map[string][]string{"dlg_1": {"att_1"}},
	}
	if ev.evidenceVersion != 3 {
		t.Fatalf("evidenceVersion = %d", ev.evidenceVersion)
	}
	if len(ev.shells) != 1 || len(ev.attention) != 1 {
		t.Fatalf("maps wrong: %+v", ev)
	}
}

// ---------------------------------------------------------------------------
// delegateAttentionTransferPlan struct
// ---------------------------------------------------------------------------

func TestDelegateAttentionTransferPlanStruct(t *testing.T) {
	plan := delegateAttentionTransferPlan{
		sourceDelegateID: "dlg_1",
		sourceRef:        "local:src",
		targetDelegateID: "dlg_2",
		targetRef:        "local:tgt",
	}
	if plan.sourceDelegateID != "dlg_1" || plan.targetDelegateID != "dlg_2" {
		t.Fatalf("struct wrong: %+v", plan)
	}
}

// ---------------------------------------------------------------------------
// delegateOwedAttentionStart struct
// ---------------------------------------------------------------------------

func TestDelegateOwedAttentionStartStruct(t *testing.T) {
	start := delegateOwedAttentionStart{
		delegateID:  "dlg_1",
		parentID:    "dlg_parent",
		attentionID: "att_1",
		generation:  2,
		pendingIDs:  []string{"att_2", "att_3"},
	}
	if start.delegateID != "dlg_1" || start.attentionID != "att_1" {
		t.Fatalf("struct wrong: %+v", start)
	}
	if start.generation != 2 || len(start.pendingIDs) != 2 {
		t.Fatalf("fields wrong: %+v", start)
	}
}

// ---------------------------------------------------------------------------
// delegateLease struct
// ---------------------------------------------------------------------------

func TestDelegateLeaseStruct(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 5}
	if lease.delegateID != "dlg_1" || lease.generation != 5 {
		t.Fatalf("struct wrong: %+v", lease)
	}
}

// ---------------------------------------------------------------------------
// delegateMutationPlans struct
// ---------------------------------------------------------------------------

func TestDelegateMutationPlansStruct(t *testing.T) {
	plans := delegateMutationPlans{
		updates: []delegateUpdatePlan{{rows: []delegateSnapshot{}}},
	}
	if len(plans.updates) != 1 {
		t.Fatalf("expected 1 update plan")
	}
}

// ---------------------------------------------------------------------------
// executeDelegateAttentionCleanup stabilize with nil runtime
// ---------------------------------------------------------------------------

func TestExecuteDelegateAttentionCleanupStabilizeNilRuntime(t *testing.T) {
	c := &delegateTreeController{}
	plan := delegateAttentionCleanupPlan{
		stabilize: true,
		runtime:   nil,
	}
	err := c.executeDelegateAttentionCleanup(plan)
	if err == nil || !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("expected errDelegateDeliveryReceiverUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// resolveDelegateAttentionDurably with nil runtime and nil controller open
// ---------------------------------------------------------------------------

func TestResolveDelegateAttentionDurablyNilRuntimeInvalidRef(t *testing.T) {
	c := &delegateTreeController{stateDir: "/nonexistent"}
	err := c.resolveDelegateAttentionDurably(nil, "invalid-ref", []string{"att_1"}, delegateAttentionDiscarded)
	if err == nil {
		t.Fatalf("expected error for invalid ref with nil runtime")
	}
}

// ---------------------------------------------------------------------------
// fmt usage
// ---------------------------------------------------------------------------

var _ = fmt.Sprintf
var _ = json.Marshal
