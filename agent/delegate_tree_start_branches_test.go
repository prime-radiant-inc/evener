package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/task"
)

// ---------------------------------------------------------------------------
// delegateCapacityKind constants
// ---------------------------------------------------------------------------

func TestDelegateCapacityKind(t *testing.T) {
	if delegateTurnCapacity != 0 {
		t.Fatalf("delegateTurnCapacity = %d, want 0", delegateTurnCapacity)
	}
	if delegateDriveCapacity != 1 {
		t.Fatalf("delegateDriveCapacity = %d, want 1", delegateDriveCapacity)
	}
}

// ---------------------------------------------------------------------------
// delegateCommittedStartFailureError
// ---------------------------------------------------------------------------

func TestDelegateCommittedStartFailureError(t *testing.T) {
	cause := errors.New("underlying error")
	err := &delegateCommittedStartFailureError{
		disposition: delegateCommittedStartFailureStopWon,
		cause:       cause,
	}
	if err.Error() != "underlying error" {
		t.Fatalf("error = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatalf("expected Unwrap to return cause")
	}
}

func TestDelegateCommittedStartFailureErrorAppendFailed(t *testing.T) {
	cause := errors.New("append failed")
	err := &delegateCommittedStartFailureError{
		disposition: delegateCommittedStartFailureAppendFailed,
		cause:       cause,
	}
	if err.Error() != "append failed" {
		t.Fatalf("error = %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// committedStartFailureDisposition
// ---------------------------------------------------------------------------

func TestCommittedStartFailureDisposition(t *testing.T) {
	t.Run("with failure error", func(t *testing.T) {
		err := &delegateCommittedStartFailureError{
			disposition: delegateCommittedStartFailureStopWon,
			cause:       errors.New("stop won"),
		}
		if d := committedStartFailureDisposition(err); d != delegateCommittedStartFailureStopWon {
			t.Fatalf("disposition = %d, want %d", d, delegateCommittedStartFailureStopWon)
		}
	})
	t.Run("without failure error", func(t *testing.T) {
		err := errors.New("plain error")
		if d := committedStartFailureDisposition(err); d != 0 {
			t.Fatalf("disposition = %d, want 0", d)
		}
	})
	t.Run("nil error", func(t *testing.T) {
		if d := committedStartFailureDisposition(nil); d != 0 {
			t.Fatalf("disposition = %d, want 0", d)
		}
	})
}

// ---------------------------------------------------------------------------
// normalizeDelegateStartFailure
// ---------------------------------------------------------------------------

func TestNormalizeDelegateStartFailure(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("all fields empty", func(t *testing.T) {
		f := normalizeDelegateStartFailure(delegateFinish{}, "fallback_reason", "fallback message", now)
		if f.outcome != delegatestore.OutcomeFailed {
			t.Fatalf("outcome = %v, want failed", f.outcome)
		}
		if f.reason != "fallback_reason" {
			t.Fatalf("reason = %q", f.reason)
		}
		if !f.endedAt.Equal(now) {
			t.Fatalf("endedAt = %v, want %v", f.endedAt, now)
		}
		if f.packet == nil {
			t.Fatalf("expected packet to be set")
		}
		if f.packet.Kind != delegatestore.PacketTerminalError {
			t.Fatalf("packet kind = %v", f.packet.Kind)
		}
		var msg string
		if err := json.Unmarshal(f.packet.Message, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg != "fallback message" {
			t.Fatalf("message = %q", msg)
		}
	})
	t.Run("all fields provided", func(t *testing.T) {
		packet := &delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"custom"`)}
		f := normalizeDelegateStartFailure(delegateFinish{
			outcome: delegatestore.OutcomeCompleted,
			reason:  "custom_reason",
			endedAt: now.Add(-time.Hour),
			packet:  packet,
		}, "fallback", "fallback msg", now)
		if f.outcome != delegatestore.OutcomeCompleted {
			t.Fatalf("outcome = %v", f.outcome)
		}
		if f.reason != "custom_reason" {
			t.Fatalf("reason = %q", f.reason)
		}
		if !f.endedAt.Equal(now.Add(-time.Hour)) {
			t.Fatalf("endedAt = %v", f.endedAt)
		}
		if f.packet != packet {
			t.Fatalf("packet should be preserved")
		}
	})
	t.Run("whitespace reason", func(t *testing.T) {
		f := normalizeDelegateStartFailure(delegateFinish{reason: "   "}, "fallback", "msg", now)
		if f.reason != "fallback" {
			t.Fatalf("reason = %q, want 'fallback'", f.reason)
		}
	})
}

// ---------------------------------------------------------------------------
// terminalFinishBatch
// ---------------------------------------------------------------------------

func TestTerminalFinishBatch(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 3}
	now := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"test"`)}

	terminal, finish := terminalFinishBatch(lease, delegatestore.OutcomeFailed, "test_reason", now, packet)

	if terminal.Kind != delegatestore.EventDelegateTerminalPrepared {
		t.Fatalf("terminal kind = %v", terminal.Kind)
	}
	if terminal.DelegateID != "dlg_1" {
		t.Fatalf("delegateID = %q", terminal.DelegateID)
	}
	if terminal.TerminalPrepared == nil {
		t.Fatalf("expected TerminalPrepared")
	}
	if terminal.TerminalPrepared.Generation != 3 {
		t.Fatalf("generation = %d", terminal.TerminalPrepared.Generation)
	}
	if terminal.TerminalPrepared.Packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("packet kind = %v", terminal.TerminalPrepared.Packet.Kind)
	}

	if finish.Kind != delegatestore.EventDelegateRunFinished {
		t.Fatalf("finish kind = %v", finish.Kind)
	}
	if finish.RunFinished == nil {
		t.Fatalf("expected RunFinished")
	}
	if finish.RunFinished.Outcome.Status != delegatestore.OutcomeFailed {
		t.Fatalf("outcome = %v", finish.RunFinished.Outcome.Status)
	}
	if finish.RunFinished.Outcome.Reason != "test_reason" {
		t.Fatalf("reason = %q", finish.RunFinished.Outcome.Reason)
	}
	if !finish.RunFinished.Outcome.EndedAt.Equal(now) {
		t.Fatalf("endedAt = %v", finish.RunFinished.Outcome.EndedAt)
	}
	if finish.RunFinished.Disposition != delegatestore.DispositionTerminalError {
		t.Fatalf("disposition = %v", finish.RunFinished.Disposition)
	}
	if finish.RunFinished.DeliveryID != delegateDeliveryID("dlg_1", 3) {
		t.Fatalf("deliveryID = %q", finish.RunFinished.DeliveryID)
	}
}

// ---------------------------------------------------------------------------
// delegateControllerRunStartedEvent
// ---------------------------------------------------------------------------

func TestDelegateControllerRunStartedEvent(t *testing.T) {
	now := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	event := delegateControllerRunStartedEvent("dlg_1", 5, delegatestore.TriggerAttention, now)
	if event.Kind != delegatestore.EventDelegateRunStarted {
		t.Fatalf("kind = %v", event.Kind)
	}
	if event.DelegateID != "dlg_1" {
		t.Fatalf("delegateID = %q", event.DelegateID)
	}
	if event.RunStarted == nil {
		t.Fatalf("expected RunStarted")
	}
	if event.RunStarted.Generation != 5 {
		t.Fatalf("generation = %d", event.RunStarted.Generation)
	}
	if event.RunStarted.Trigger != delegatestore.TriggerAttention {
		t.Fatalf("trigger = %v", event.RunStarted.Trigger)
	}
	if !event.RunStarted.StartedAt.Equal(now) {
		t.Fatalf("startedAt = %v", event.RunStarted.StartedAt)
	}
}

// ---------------------------------------------------------------------------
// cloneDelegateStartDescriptor
// ---------------------------------------------------------------------------

func TestCloneDelegateStartDescriptor(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		desc := delegatestore.Descriptor{
			Task:       "do something",
			AgentType:  "default",
			Resumable:  true,
			WorkingDir: "/path",
		}
		clone := cloneDelegateStartDescriptor(desc)
		if clone.Task != "do something" || clone.AgentType != "default" {
			t.Fatalf("clone fields wrong: %+v", clone)
		}
	})
	t.Run("slices deep-copied", func(t *testing.T) {
		desc := delegatestore.Descriptor{
			TaskTemplates:      []task.TaskTemplate{{Title: "t1"}},
			ToolNameCeiling:    []string{"tool1"},
			FrozenSkillNames:   []string{"skill1"},
			FrozenSkillBodies:  []string{"body1"},
			ResultSchema:       json.RawMessage(`{"type":"object"}`),
			ExplicitToolGrants: []string{"grant1"},
		}
		clone := cloneDelegateStartDescriptor(desc)
		// Modify original
		desc.TaskTemplates[0] = task.TaskTemplate{Title: "modified"}
		desc.ToolNameCeiling[0] = "modified"
		desc.FrozenSkillNames[0] = "modified"
		desc.FrozenSkillBodies[0] = "modified"
		desc.ExplicitToolGrants[0] = "modified"
		// Verify clone is unaffected
		if clone.TaskTemplates[0].Title != "t1" {
			t.Fatalf("TaskTemplates not deep-copied")
		}
		if clone.ToolNameCeiling[0] != "tool1" {
			t.Fatalf("ToolNameCeiling not deep-copied")
		}
		if clone.FrozenSkillNames[0] != "skill1" {
			t.Fatalf("FrozenSkillNames not deep-copied")
		}
		if clone.FrozenSkillBodies[0] != "body1" {
			t.Fatalf("FrozenSkillBodies not deep-copied")
		}
		if clone.ExplicitToolGrants[0] != "grant1" {
			t.Fatalf("ExplicitToolGrants not deep-copied")
		}
	})
	t.Run("sandbox deep-copied", func(t *testing.T) {
		network := true
		desc := delegatestore.Descriptor{
			Sandbox: &delegatestore.SandboxSnapshot{
				Network:            &network,
				DenylistAdd:        []string{"x"},
				DenylistRemove:     []string{"y"},
				ExtraWritableRoots: []string{"/w"},
				ExtraReadRoots:     []string{"/r"},
			},
		}
		clone := cloneDelegateStartDescriptor(desc)
		if clone.Sandbox == nil {
			t.Fatalf("expected sandbox to be cloned")
		}
		if clone.Sandbox.Network == nil {
			t.Fatalf("expected network to be cloned")
		}
		// Modify original
		desc.Sandbox.DenylistAdd[0] = "modified"
		*desc.Sandbox.Network = false
		// Verify clone is unaffected
		if clone.Sandbox.DenylistAdd[0] != "x" {
			t.Fatalf("DenylistAdd not deep-copied")
		}
		if *clone.Sandbox.Network != true {
			t.Fatalf("Network not deep-copied")
		}
	})
	t.Run("nil sandbox", func(t *testing.T) {
		desc := delegatestore.Descriptor{}
		clone := cloneDelegateStartDescriptor(desc)
		if clone.Sandbox != nil {
			t.Fatalf("expected nil sandbox preserved")
		}
	})
	t.Run("provenance cloned", func(t *testing.T) {
		desc := delegatestore.Descriptor{
			Provenance: &provenance.Causal{ChainTruncated: true},
		}
		clone := cloneDelegateStartDescriptor(desc)
		if clone.Provenance == nil {
			t.Fatalf("expected provenance to be cloned")
		}
		if !clone.Provenance.ChainTruncated {
			t.Fatalf("expected ChainTruncated to be preserved")
		}
	})
}

// ---------------------------------------------------------------------------
// reserveCapacityLocked / releaseCapacityLocked
// ---------------------------------------------------------------------------

func TestReserveAndReleaseCapacityLocked(t *testing.T) {
	t.Run("turn capacity unlimited", func(t *testing.T) {
		c := &delegateTreeController{}
		if !c.reserveCapacityLocked(delegateTurnCapacity) {
			t.Fatalf("expected reserve to succeed")
		}
		if !c.reserveCapacityLocked(delegateTurnCapacity) {
			t.Fatalf("expected second reserve to succeed (unlimited)")
		}
		if c.turnsInUse != 2 {
			t.Fatalf("turnsInUse = %d, want 2", c.turnsInUse)
		}
		c.releaseCapacityLocked(delegateTurnCapacity)
		if c.turnsInUse != 1 {
			t.Fatalf("turnsInUse = %d, want 1", c.turnsInUse)
		}
	})
	t.Run("turn capacity limited", func(t *testing.T) {
		c := &delegateTreeController{turnLimit: 2}
		if !c.reserveCapacityLocked(delegateTurnCapacity) {
			t.Fatalf("expected first reserve to succeed")
		}
		if !c.reserveCapacityLocked(delegateTurnCapacity) {
			t.Fatalf("expected second reserve to succeed")
		}
		if c.reserveCapacityLocked(delegateTurnCapacity) {
			t.Fatalf("expected third reserve to fail (at limit)")
		}
		c.releaseCapacityLocked(delegateTurnCapacity)
		if !c.reserveCapacityLocked(delegateTurnCapacity) {
			t.Fatalf("expected reserve to succeed after release")
		}
	})
	t.Run("drive capacity unlimited", func(t *testing.T) {
		c := &delegateTreeController{}
		if !c.reserveCapacityLocked(delegateDriveCapacity) {
			t.Fatalf("expected reserve to succeed")
		}
		if c.drivesInUse != 1 {
			t.Fatalf("drivesInUse = %d, want 1", c.drivesInUse)
		}
		c.releaseCapacityLocked(delegateDriveCapacity)
		if c.drivesInUse != 0 {
			t.Fatalf("drivesInUse = %d, want 0", c.drivesInUse)
		}
	})
	t.Run("drive capacity limited", func(t *testing.T) {
		c := &delegateTreeController{driveLimit: 1}
		if !c.reserveCapacityLocked(delegateDriveCapacity) {
			t.Fatalf("expected first reserve to succeed")
		}
		if c.reserveCapacityLocked(delegateDriveCapacity) {
			t.Fatalf("expected second reserve to fail (at limit)")
		}
		c.releaseCapacityLocked(delegateDriveCapacity)
		if !c.reserveCapacityLocked(delegateDriveCapacity) {
			t.Fatalf("expected reserve to succeed after release")
		}
	})
	t.Run("release below zero guarded", func(t *testing.T) {
		c := &delegateTreeController{}
		c.releaseCapacityLocked(delegateTurnCapacity)
		if c.turnsInUse != 0 {
			t.Fatalf("turnsInUse = %d, want 0 (guarded)", c.turnsInUse)
		}
		c.releaseCapacityLocked(delegateDriveCapacity)
		if c.drivesInUse != 0 {
			t.Fatalf("drivesInUse = %d, want 0 (guarded)", c.drivesInUse)
		}
	})
}

// ---------------------------------------------------------------------------
// reservedAttentionID nil controller
// ---------------------------------------------------------------------------

func TestReservedAttentionIDNilController(t *testing.T) {
	var c *delegateTreeController
	if c.reservedAttentionID(nil) != "" {
		t.Fatalf("expected empty for nil controller")
	}
}

func TestReservedAttentionIDNilRuntime(t *testing.T) {
	c := &delegateTreeController{}
	if c.reservedAttentionID(nil) != "" {
		t.Fatalf("expected empty for nil runtime")
	}
}

// ---------------------------------------------------------------------------
// delegateStartReservation struct
// ---------------------------------------------------------------------------

func TestDelegateStartReservationStruct(t *testing.T) {
	r := &delegateStartReservation{
		delegateID:     "dlg_1",
		transcriptPath: "/path/transcript.jsonl",
		worktreePath:   "/path/worktree",
	}
	if r.delegateID != "dlg_1" || r.transcriptPath != "/path/transcript.jsonl" {
		t.Fatalf("struct wrong: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// delegateStartRecord struct
// ---------------------------------------------------------------------------

func TestDelegateStartRecordStruct(t *testing.T) {
	r := &delegateStartRecord{
		delegateID:   "dlg_1",
		generation:   2,
		trigger:      delegatestore.TriggerAttention,
		capacityKind: delegateDriveCapacity,
		create:       false,
		attentionID:  "att_1",
	}
	if r.delegateID != "dlg_1" || r.generation != 2 {
		t.Fatalf("struct wrong: %+v", r)
	}
	if r.trigger != delegatestore.TriggerAttention || r.capacityKind != delegateDriveCapacity {
		t.Fatalf("struct wrong: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// delegateStartCommit struct
// ---------------------------------------------------------------------------

func TestDelegateStartCommitStruct(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 3}
	c := delegateStartCommit{
		lease:          lease,
		descriptor:     delegatestore.Descriptor{Task: "test"},
		transcriptPath: "/path",
		worktreePath:   "/wt",
	}
	if c.lease.delegateID != "dlg_1" || c.lease.generation != 3 {
		t.Fatalf("lease wrong: %+v", c.lease)
	}
}

// ---------------------------------------------------------------------------
// delegateFinish struct
// ---------------------------------------------------------------------------

func TestDelegateFinishStruct(t *testing.T) {
	f := delegateFinish{
		outcome:          delegatestore.OutcomeCompleted,
		disposition:      delegatestore.DispositionReported,
		reason:           "done",
		endedAt:          time.Now(),
		exhaustionBudget: delegatestore.ExhaustionBudgetTurns,
		exhaustionLimit:  10,
	}
	if f.outcome != delegatestore.OutcomeCompleted {
		t.Fatalf("outcome = %v", f.outcome)
	}
	if f.exhaustionLimit != 10 {
		t.Fatalf("limit = %d", f.exhaustionLimit)
	}
}

// ---------------------------------------------------------------------------
// delegateInputClaim struct
// ---------------------------------------------------------------------------

func TestDelegateInputClaimStruct(t *testing.T) {
	claim := delegateInputClaim{
		lease: delegateLease{delegateID: "dlg_1", generation: 1},
		token: 42,
	}
	if claim.lease.delegateID != "dlg_1" || claim.token != 42 {
		t.Fatalf("struct wrong: %+v", claim)
	}
}

// ---------------------------------------------------------------------------
// delegateCommittedStartFailureDisposition constants
// ---------------------------------------------------------------------------

func TestDelegateCommittedStartFailureDispositionConstants(t *testing.T) {
	if delegateCommittedStartFailureStopWon != 1 {
		t.Fatalf("delegateCommittedStartFailureStopWon = %d, want 1", delegateCommittedStartFailureStopWon)
	}
	if delegateCommittedStartFailureAppendFailed != 2 {
		t.Fatalf("delegateCommittedStartFailureAppendFailed = %d, want 2", delegateCommittedStartFailureAppendFailed)
	}
}

// ---------------------------------------------------------------------------
// normalizeDelegateStartFailure: long message truncation
// ---------------------------------------------------------------------------

func TestNormalizeDelegateStartFailureLongMessage(t *testing.T) {
	now := time.Now()
	longMessage := strings.Repeat("x", 600)
	f := normalizeDelegateStartFailure(delegateFinish{reason: longMessage}, "fallback", longMessage, now)
	// normalizeDelegateStartFailure does not truncate the reason; a long
	// reason must be preserved intact.
	if len(f.reason) != len(longMessage) {
		t.Fatalf("reason truncated: got len %d, want %d", len(f.reason), len(longMessage))
	}
	// The packet message should be the full message (json.Marshal doesn't truncate).
	if f.packet == nil {
		t.Fatalf("expected packet")
	}
}

// ---------------------------------------------------------------------------
// inputPersistFailureBatch
// ---------------------------------------------------------------------------

func TestInputPersistFailureBatch(t *testing.T) {
	c := &delegateTreeController{now: func() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }}
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	terminal, finish := c.inputPersistFailureBatch(lease, errors.New("disk full"))
	if terminal.Kind != delegatestore.EventDelegateTerminalPrepared {
		t.Fatalf("terminal kind = %v", terminal.Kind)
	}
	if terminal.TerminalPrepared == nil {
		t.Fatalf("expected TerminalPrepared")
	}
	if terminal.TerminalPrepared.Packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("packet kind = %v", terminal.TerminalPrepared.Packet.Kind)
	}
	var msg string
	if err := json.Unmarshal(terminal.TerminalPrepared.Packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(msg, "input persistence failed") {
		t.Fatalf("message = %q", msg)
	}
	if !strings.Contains(msg, "disk full") {
		t.Fatalf("expected error in message: %q", msg)
	}
	if finish.Kind != delegatestore.EventDelegateRunFinished {
		t.Fatalf("finish kind = %v", finish.Kind)
	}
	if finish.RunFinished.Outcome.Status != delegatestore.OutcomeFailed {
		t.Fatalf("outcome = %v", finish.RunFinished.Outcome.Status)
	}
	if finish.RunFinished.Outcome.Reason != "input_persist_failed" {
		t.Fatalf("reason = %q", finish.RunFinished.Outcome.Reason)
	}
}

func TestInputPersistFailureBatchNilErr(t *testing.T) {
	c := &delegateTreeController{now: func() time.Time { return time.Now() }}
	lease := delegateLease{delegateID: "dlg_2", generation: 2}
	terminal, _ := c.inputPersistFailureBatch(lease, nil)
	var msg string
	if err := json.Unmarshal(terminal.TerminalPrepared.Packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg != "input persistence failed" {
		t.Fatalf("message = %q, want 'input persistence failed'", msg)
	}
}

func TestInputPersistFailureBatchLongMessage(t *testing.T) {
	c := &delegateTreeController{now: func() time.Time { return time.Now() }}
	lease := delegateLease{delegateID: "dlg_3", generation: 3}
	longErr := errors.New(strings.Repeat("x", 600))
	terminal, _ := c.inputPersistFailureBatch(lease, longErr)
	var msg string
	if err := json.Unmarshal(terminal.TerminalPrepared.Packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Message should be truncated to 512 chars (plus the prefix)
	if len(msg) > 600 {
		t.Fatalf("message should be truncated, len = %d", len(msg))
	}
}

// ---------------------------------------------------------------------------
// startInputFailureBatch
// ---------------------------------------------------------------------------

func TestStartInputFailureBatch(t *testing.T) {
	c := &delegateTreeController{now: func() time.Time { return time.Now() }}
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	terminal, finish := c.startInputFailureBatch(lease, delegateFinish{})
	if terminal.Kind != delegatestore.EventDelegateTerminalPrepared {
		t.Fatalf("terminal kind = %v", terminal.Kind)
	}
	if finish.RunFinished.Outcome.Status != delegatestore.OutcomeFailed {
		t.Fatalf("outcome = %v, want failed (default)", finish.RunFinished.Outcome.Status)
	}
	if finish.RunFinished.Outcome.Reason != "input_persist_failed" {
		t.Fatalf("reason = %q", finish.RunFinished.Outcome.Reason)
	}
}

// ---------------------------------------------------------------------------
// committedStartFailureBatch
// ---------------------------------------------------------------------------

func TestCommittedStartFailureBatch(t *testing.T) {
	c := &delegateTreeController{now: func() time.Time { return time.Now() }}
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	terminal, finish := c.committedStartFailureBatch(lease, delegateFinish{})
	if terminal.Kind != delegatestore.EventDelegateTerminalPrepared {
		t.Fatalf("terminal kind = %v", terminal.Kind)
	}
	if finish.RunFinished.Outcome.Status != delegatestore.OutcomeFailed {
		t.Fatalf("outcome = %v, want failed (default)", finish.RunFinished.Outcome.Status)
	}
	if finish.RunFinished.Outcome.Reason != "construction_failed" {
		t.Fatalf("reason = %q", finish.RunFinished.Outcome.Reason)
	}
}

// ---------------------------------------------------------------------------
// hasAttentionStartReservationLocked
// ---------------------------------------------------------------------------

func TestHasAttentionStartReservationLockedEmpty(t *testing.T) {
	c := &delegateTreeController{reservations: map[uint64]*delegateStartRecord{}}
	if c.hasAttentionStartReservationLocked("dlg_1") {
		t.Fatalf("expected false with no reservations")
	}
}

func TestHasAttentionStartReservationLockedWithMatch(t *testing.T) {
	c := &delegateTreeController{
		reservations: map[uint64]*delegateStartRecord{
			1: {delegateID: "dlg_1", trigger: delegatestore.TriggerAttention},
		},
	}
	if !c.hasAttentionStartReservationLocked("dlg_1") {
		t.Fatalf("expected true with matching attention reservation")
	}
}

func TestHasAttentionStartReservationLockedWrongTrigger(t *testing.T) {
	c := &delegateTreeController{
		reservations: map[uint64]*delegateStartRecord{
			1: {delegateID: "dlg_1", trigger: delegatestore.TriggerOwnerInput},
		},
	}
	if c.hasAttentionStartReservationLocked("dlg_1") {
		t.Fatalf("expected false for non-attention trigger")
	}
}

func TestHasAttentionStartReservationLockedWrongID(t *testing.T) {
	c := &delegateTreeController{
		reservations: map[uint64]*delegateStartRecord{
			1: {delegateID: "dlg_other", trigger: delegatestore.TriggerAttention},
		},
	}
	if c.hasAttentionStartReservationLocked("dlg_1") {
		t.Fatalf("expected false for different delegate ID")
	}
}

// ---------------------------------------------------------------------------
// reservationRecordLocked
// ---------------------------------------------------------------------------

func TestReservationRecordLockedNilReservation(t *testing.T) {
	c := &delegateTreeController{}
	_, err := c.reservationRecordLocked(nil)
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("expected errDelegateTargetBusy for nil reservation, got %v", err)
	}
}

func TestReservationRecordLockedNotFound(t *testing.T) {
	c := &delegateTreeController{
		reservations: map[uint64]*delegateStartRecord{},
	}
	reservation := &delegateStartReservation{delegateID: "dlg_1"}
	_, err := c.reservationRecordLocked(reservation)
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("expected errDelegateTargetBusy for not found, got %v", err)
	}
}

func TestReservationRecordLockedFound(t *testing.T) {
	reservation := &delegateStartReservation{delegateID: "dlg_1"}
	record := &delegateStartRecord{
		receipt:    reservation,
		delegateID: "dlg_1",
		token:      42,
	}
	c := &delegateTreeController{
		reservations: map[uint64]*delegateStartRecord{42: record},
	}
	found, err := c.reservationRecordLocked(reservation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.delegateID != "dlg_1" {
		t.Fatalf("delegateID = %q", found.delegateID)
	}
}
