package delegatestore

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

// ---------------------------------------------------------------------------
// validateEventEnvelope
// ---------------------------------------------------------------------------

func TestValidateEventEnvelope_EmptyDelegateID(t *testing.T) {
	err := validateEventEnvelope(Event{Kind: EventDelegateCreated})
	if err == nil || !strings.Contains(err.Error(), "empty delegate id") {
		t.Fatalf("expected empty delegate id error, got %v", err)
	}
}

func TestValidateEventEnvelope_UnknownKind(t *testing.T) {
	err := validateEventEnvelope(Event{Kind: "unknown", DelegateID: "dlg_1"})
	if err == nil || !strings.Contains(err.Error(), "unknown delegate event kind") {
		t.Fatalf("expected unknown kind error, got %v", err)
	}
}

func TestValidateEventEnvelope_PayloadMismatch(t *testing.T) {
	// Kind says Created but no Created payload.
	err := validateEventEnvelope(Event{Kind: EventDelegateCreated, DelegateID: "dlg_1", RunStarted: &RunStarted{}})
	if err == nil || !strings.Contains(err.Error(), "payload does not match kind") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}

func TestValidateEventEnvelope_MultiplePayloads(t *testing.T) {
	err := validateEventEnvelope(Event{
		Kind:       EventDelegateCreated,
		DelegateID: "dlg_1",
		Created:    &DelegateCreated{},
		RunStarted: &RunStarted{},
	})
	if err == nil || !strings.Contains(err.Error(), "payload does not match kind") {
		t.Fatalf("expected multiple-payloads error, got %v", err)
	}
}

func TestValidateEventEnvelope_Valid(t *testing.T) {
	err := validateEventEnvelope(Event{
		Kind:       EventDelegateCreated,
		DelegateID: "dlg_1",
		Created:    &DelegateCreated{Descriptor: Descriptor{}},
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// validateDescriptor
// ---------------------------------------------------------------------------

func TestValidateDescriptor_EmptyChildSessionID(t *testing.T) {
	err := validateDescriptor(Descriptor{})
	if err == nil || !strings.Contains(err.Error(), "child session id is empty") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateDescriptor_EmptyTranscriptRef(t *testing.T) {
	err := validateDescriptor(Descriptor{ChildSessionID: "sess_1"})
	if err == nil || !strings.Contains(err.Error(), "transcript ref is empty") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateDescriptor_EmptyOwnerSessionID(t *testing.T) {
	err := validateDescriptor(Descriptor{ChildSessionID: "sess_1", TranscriptRef: "local:sess_1"})
	if err == nil || !strings.Contains(err.Error(), "owner session id is empty") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateDescriptor_EmptyTask(t *testing.T) {
	err := validateDescriptor(Descriptor{ChildSessionID: "sess_1", TranscriptRef: "local:sess_1", OwnerSessionID: "root"})
	if err == nil || !strings.Contains(err.Error(), "task is empty") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateDescriptor_EmptyAgentType(t *testing.T) {
	err := validateDescriptor(Descriptor{ChildSessionID: "sess_1", TranscriptRef: "local:sess_1", OwnerSessionID: "root", Task: "do it"})
	if err == nil || !strings.Contains(err.Error(), "agent type is empty") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateDescriptor_InvalidResultSchema(t *testing.T) {
	err := validateDescriptor(Descriptor{
		ChildSessionID:  "sess_1",
		TranscriptRef:   "local:sess_1",
		OwnerSessionID:  "root",
		Task:            "do it",
		AgentType:       "general",
		ToolNameCeiling: []string{"communicate"},
		ResultSchema:    json.RawMessage(`{invalid`),
	})
	if err == nil || !strings.Contains(err.Error(), "result schema is not valid JSON") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateDescriptor_SharedTaskStoreWithoutSharing(t *testing.T) {
	err := validateDescriptor(Descriptor{
		ChildSessionID:                "sess_1",
		TranscriptRef:                 "local:sess_1",
		OwnerSessionID:                "root",
		Task:                          "do it",
		AgentType:                     "general",
		ToolNameCeiling:               []string{"communicate"},
		SharedTaskStoreOwnerSessionID: "other",
	})
	if err == nil || !strings.Contains(err.Error(), "shared task store owner requires task sharing") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateDescriptor_SharedTaskStoreWithSharingButEmpty(t *testing.T) {
	err := validateDescriptor(Descriptor{
		ChildSessionID:  "sess_1",
		TranscriptRef:   "local:sess_1",
		OwnerSessionID:  "root",
		Task:            "do it",
		AgentType:       "general",
		ToolNameCeiling: []string{"communicate"},
		Config:          schema.ConfigSnapshot{ShareTasksWithChildren: true},
	})
	if err == nil || !strings.Contains(err.Error(), "shared task store owner session id is empty") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// validateToolNameCeiling
// ---------------------------------------------------------------------------

func TestValidateToolNameCeiling_Empty(t *testing.T) {
	err := validateToolNameCeiling(Descriptor{ToolNameCeiling: nil})
	if err == nil || !strings.Contains(err.Error(), "tool name ceiling is empty") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateToolNameCeiling_EmptyName(t *testing.T) {
	err := validateToolNameCeiling(Descriptor{ToolNameCeiling: []string{""}})
	if err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateToolNameCeiling_Wildcard(t *testing.T) {
	err := validateToolNameCeiling(Descriptor{ToolNameCeiling: []string{"*"}})
	if err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateToolNameCeiling_Duplicate(t *testing.T) {
	err := validateToolNameCeiling(Descriptor{ToolNameCeiling: []string{"communicate", "communicate"}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateToolNameCeiling_ResultToolNotInCeiling(t *testing.T) {
	err := validateToolNameCeiling(Descriptor{
		ToolNameCeiling: []string{"read_file"},
		Config:          schema.ConfigSnapshot{ResultToolName: "communicate"},
	})
	if err == nil || !strings.Contains(err.Error(), "result tool") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateToolNameCeiling_ResultToolInCeiling(t *testing.T) {
	err := validateToolNameCeiling(Descriptor{
		ToolNameCeiling: []string{"communicate", "read_file"},
		Config:          schema.ConfigSnapshot{ResultToolName: "communicate"},
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// validateSandboxConfigProjection
// ---------------------------------------------------------------------------

func TestValidateSandboxConfigProjection_NilSnapshotNonEmptyConfig(t *testing.T) {
	err := validateSandboxConfigProjection(Descriptor{Config: schema.ConfigSnapshot{Sandbox: "strict"}})
	if err == nil || !strings.Contains(err.Error(), "snapshot is nil but config projection is nonempty") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateSandboxConfigProjection_SnapshotWithoutConfig(t *testing.T) {
	err := validateSandboxConfigProjection(Descriptor{Sandbox: &SandboxSnapshot{Mode: "strict"}})
	if err == nil || !strings.Contains(err.Error(), "snapshot requires a config projection") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateSandboxConfigProjection_ModeMismatch(t *testing.T) {
	err := validateSandboxConfigProjection(Descriptor{
		Sandbox: &SandboxSnapshot{Mode: "strict"},
		Config:  schema.ConfigSnapshot{Sandbox: "permissive"},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match config mode") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestValidateSandboxConfigProjection_NetworkMismatch(t *testing.T) {
	netTrue := true
	err := validateSandboxConfigProjection(Descriptor{
		Sandbox: &SandboxSnapshot{Mode: "strict"},
		Config:  schema.ConfigSnapshot{Sandbox: "strict", SandboxNet: &netTrue},
	})
	if err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// applySubtreeStopRequested
// ---------------------------------------------------------------------------

func TestApplySubtreeStopRequested_ZeroSeq(t *testing.T) {
	err := applySubtreeStopRequested(State{}, Event{
		Kind:                 EventDelegateSubtreeStopRequested,
		DelegateID:           "dlg_1",
		SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "sequence is zero") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestApplySubtreeStopRequested_TargetMismatch(t *testing.T) {
	err := applySubtreeStopRequested(State{}, Event{
		Seq:                  1,
		DelegateID:           "dlg_1",
		SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_other"},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestApplySubtreeStopRequested_NonexistentTarget(t *testing.T) {
	err := applySubtreeStopRequested(State{}, Event{
		Seq:                  1,
		DelegateID:           "dlg_1",
		SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_1"},
	})
	if err == nil {
		t.Fatal("expected error for nonexistent target")
	}
}

func TestApplySubtreeStopRequested_AlreadyPendingStop(t *testing.T) {
	state := State{
		"dlg_1": {Phase: PhaseRunning, Resumable: true},
		"dlg_2": {Phase: PhaseRunning, PendingStopSeq: 5, Resumable: true},
	}
	err := applySubtreeStopRequested(state, Event{
		Seq:                  1,
		DelegateID:           "dlg_1",
		SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestApplySubtreeStopRequested_Success(t *testing.T) {
	state := State{
		"dlg_1":     {Phase: PhaseRunning, Resumable: true, Descriptor: Descriptor{ParentDelegateID: ""}},
		"dlg_child": {Phase: PhaseRunning, Resumable: true, Descriptor: Descriptor{ParentDelegateID: "dlg_1"}},
		"dlg_other": {Phase: PhaseRunning, Resumable: true},
	}
	err := applySubtreeStopRequested(state, Event{
		Seq:                  3,
		DelegateID:           "dlg_1",
		SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state["dlg_1"].PendingStopSeq != 3 || state["dlg_1"].Phase != PhaseStopping {
		t.Fatalf("dlg_1 not stopped: %#v", state["dlg_1"])
	}
	if state["dlg_child"].PendingStopSeq != 3 || state["dlg_child"].Phase != PhaseStopping {
		t.Fatalf("dlg_child not stopped: %#v", state["dlg_child"])
	}
	if state["dlg_other"].PendingStopSeq != 0 {
		t.Fatalf("dlg_other should not be stopped: %#v", state["dlg_other"])
	}
}

// ---------------------------------------------------------------------------
// applySubtreeStopCompleted
// ---------------------------------------------------------------------------

func TestApplySubtreeStopCompleted_ZeroRequestSeq(t *testing.T) {
	err := applySubtreeStopCompleted(State{}, Event{
		DelegateID:           "dlg_1",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 0},
	})
	if err == nil || !strings.Contains(err.Error(), "sequence is zero") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestApplySubtreeStopCompleted_PendingSeqMismatch(t *testing.T) {
	state := State{"dlg_1": {PendingStopSeq: 2, Resumable: true}}
	err := applySubtreeStopCompleted(state, Event{
		DelegateID:           "dlg_1",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "pending stop sequence") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestApplySubtreeStopCompleted_RunStillOpen(t *testing.T) {
	state := State{"dlg_1": {PendingStopSeq: 1, CurrentRunOpen: true, Resumable: true}}
	err := applySubtreeStopCompleted(state, Event{
		DelegateID:           "dlg_1",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "still open") {
		t.Fatalf("expected error, got %v", err)
	}
}

// TestApplySubtreeStopCompleted_NoMembers covers a case that is effectively
// unreachable: if the target's PendingStopSeq matches RequestSeq, it is itself
// a member, so covered is never empty. The branch is documented as defensive.
func TestApplySubtreeStopCompleted_NoMembers(t *testing.T) {
	// If the target's PendingStopSeq doesn't match, the mismatch error fires first.
	state := State{"dlg_1": {PendingStopSeq: 2, Resumable: true}}
	err := applySubtreeStopCompleted(state, Event{
		DelegateID:           "dlg_1",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "pending stop sequence") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}

func TestApplySubtreeStopCompleted_Success(t *testing.T) {
	state := State{
		"dlg_1":     {PendingStopSeq: 1, Resumable: true},
		"dlg_child": {PendingStopSeq: 1, Resumable: false},
	}
	err := applySubtreeStopCompleted(state, Event{
		DelegateID:           "dlg_1",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state["dlg_1"].PendingStopSeq != 0 || state["dlg_1"].Phase != PhaseIdle {
		t.Fatalf("dlg_1 not completed: %#v", state["dlg_1"])
	}
	if state["dlg_child"].PendingStopSeq != 0 || state["dlg_child"].Phase != PhaseClosed {
		t.Fatalf("dlg_child not closed: %#v", state["dlg_child"])
	}
}

// ---------------------------------------------------------------------------
// applyDeliveryAcknowledged
// ---------------------------------------------------------------------------

func TestApplyDeliveryAcknowledged_EmptyDeliveryID(t *testing.T) {
	state := State{"dlg_1": {}}
	err := applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{},
	})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestApplyDeliveryAcknowledged_NotHead(t *testing.T) {
	state := State{"dlg_1": {PendingDeliveries: []PendingDelivery{{DeliveryID: "other"}}}}
	err := applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "target"},
	})
	if err == nil || !strings.Contains(err.Error(), "not the pending head") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestApplyDeliveryAcknowledged_Success(t *testing.T) {
	state := State{"dlg_1": {PendingDeliveries: []PendingDelivery{
		{DeliveryID: "head"},
		{DeliveryID: "next"},
	}}}
	err := applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "head"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state["dlg_1"].PendingDeliveries) != 1 || state["dlg_1"].PendingDeliveries[0].DeliveryID != "next" {
		t.Fatalf("unexpected deliveries: %#v", state["dlg_1"].PendingDeliveries)
	}
}

// ---------------------------------------------------------------------------
// applyAttentionChanged
// ---------------------------------------------------------------------------

func TestApplyAttentionChanged(t *testing.T) {
	t.Run("set true", func(t *testing.T) {
		state := State{"dlg_1": {NeedsAttention: false}}
		err := applyAttentionChanged(state, Event{
			DelegateID:       "dlg_1",
			AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !state["dlg_1"].NeedsAttention {
			t.Fatal("expected NeedsAttention=true")
		}
	})
	t.Run("set false", func(t *testing.T) {
		state := State{"dlg_1": {NeedsAttention: true}}
		err := applyAttentionChanged(state, Event{
			DelegateID:       "dlg_1",
			AttentionChanged: &DelegateAttentionChanged{NeedsAttention: false},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if state["dlg_1"].NeedsAttention {
			t.Fatal("expected NeedsAttention=false")
		}
	})
	t.Run("nonexistent delegate", func(t *testing.T) {
		state := State{}
		err := applyAttentionChanged(state, Event{
			DelegateID:       "dlg_1",
			AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true},
		})
		if err == nil {
			t.Fatal("expected error for nonexistent delegate")
		}
	})
}

// Ensure errors is used
var _ = errors.New
