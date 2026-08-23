package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// ---------------------------------------------------------------------------
// delegateDeliveryID
// ---------------------------------------------------------------------------

func TestDelegateDeliveryID(t *testing.T) {
	id := delegateDeliveryID("dlg_1", 3)
	if id != "dlg_1/delivery/3" {
		t.Fatalf("deliveryID = %q, want 'dlg_1/delivery/3'", id)
	}
}

func TestDelegateDeliveryIDZeroGeneration(t *testing.T) {
	id := delegateDeliveryID("dlg_1", 0)
	if id != "dlg_1/delivery/0" {
		t.Fatalf("deliveryID = %q", id)
	}
}

// ---------------------------------------------------------------------------
// delegateMissingTerminalPacket
// ---------------------------------------------------------------------------

func TestDelegateMissingTerminalPacket(t *testing.T) {
	packet := delegateMissingTerminalPacket()
	if packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("kind = %v", packet.Kind)
	}
	var msg string
	if err := json.Unmarshal(packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(msg, "without an accepted communicate result") {
		t.Fatalf("unexpected message: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// delegateStoppedTerminalPacket
// ---------------------------------------------------------------------------

func TestDelegateStoppedTerminalPacket(t *testing.T) {
	packet := delegateStoppedTerminalPacket()
	if packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("kind = %v", packet.Kind)
	}
	var msg string
	if err := json.Unmarshal(packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg != "stopped by parent" {
		t.Fatalf("expected 'stopped by parent', got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// delegateIsMissingTerminalPacket
// ---------------------------------------------------------------------------

func TestDelegateIsMissingTerminalPacketTrue(t *testing.T) {
	packet := delegateMissingTerminalPacket()
	if !delegateIsMissingTerminalPacket(packet) {
		t.Fatalf("expected true for missing terminal packet")
	}
}

func TestDelegateIsMissingTerminalPacketFalse(t *testing.T) {
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported}
	if delegateIsMissingTerminalPacket(packet) {
		t.Fatalf("expected false for reported packet")
	}
}

func TestDelegateIsMissingTerminalPacketWithStructuredResult(t *testing.T) {
	packet := delegateMissingTerminalPacket()
	packet.StructuredResult = json.RawMessage(`{}`)
	if delegateIsMissingTerminalPacket(packet) {
		t.Fatalf("expected false for packet with structured result")
	}
}

func TestDelegateIsMissingTerminalPacketWithWarnings(t *testing.T) {
	packet := delegateMissingTerminalPacket()
	packet.Warnings = []string{"warning"}
	if delegateIsMissingTerminalPacket(packet) {
		t.Fatalf("expected false for packet with warnings")
	}
}

func TestDelegateIsMissingTerminalPacketWithMetadata(t *testing.T) {
	packet := delegateMissingTerminalPacket()
	packet.Metadata = json.RawMessage(`{}`)
	if delegateIsMissingTerminalPacket(packet) {
		t.Fatalf("expected false for packet with metadata")
	}
}

// ---------------------------------------------------------------------------
// delegateTerminalErrorPacket
// ---------------------------------------------------------------------------

func TestDelegateTerminalErrorPacket(t *testing.T) {
	packet := delegateTerminalErrorPacket("some error")
	if packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("kind = %v", packet.Kind)
	}
	var msg string
	if err := json.Unmarshal(packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg != "some error" {
		t.Fatalf("expected 'some error', got %q", msg)
	}
}

func TestDelegateTerminalErrorPacketEmpty(t *testing.T) {
	packet := delegateTerminalErrorPacket("")
	var msg string
	if err := json.Unmarshal(packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg != "delegate generation ended before reporting a result" {
		t.Fatalf("expected default message for empty reason, got %q", msg)
	}
}

func TestDelegateTerminalErrorPacketTruncation(t *testing.T) {
	long := strings.Repeat("x", delegateFinishReasonLimit+100)
	packet := delegateTerminalErrorPacket(long)
	var msg string
	if err := json.Unmarshal(packet.Message, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msg) > delegateFinishReasonLimit {
		t.Fatalf("expected truncation to %d, got %d", delegateFinishReasonLimit, len(msg))
	}
}

// ---------------------------------------------------------------------------
// delegatePacketDisposition
// ---------------------------------------------------------------------------

func TestDelegatePacketDispositionReported(t *testing.T) {
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported}
	if d := delegatePacketDisposition(packet); d != delegatestore.DispositionReported {
		t.Fatalf("expected reported, got %v", d)
	}
}

func TestDelegatePacketDispositionTerminalError(t *testing.T) {
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError}
	if d := delegatePacketDisposition(packet); d != delegatestore.DispositionTerminalError {
		t.Fatalf("expected terminal_error, got %v", d)
	}
}

// ---------------------------------------------------------------------------
// delegatePreparedFinish
// ---------------------------------------------------------------------------

func TestDelegatePreparedFinishReported(t *testing.T) {
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported}
	finish := delegatePreparedFinish(packet)
	if finish.outcome != delegatestore.OutcomeCompleted {
		t.Fatalf("expected completed, got %v", finish.outcome)
	}
	if finish.disposition != delegatestore.DispositionReported {
		t.Fatalf("expected reported, got %v", finish.disposition)
	}
}

func TestDelegatePreparedFinishMissingTerminal(t *testing.T) {
	packet := delegateMissingTerminalPacket()
	finish := delegatePreparedFinish(packet)
	if finish.outcome != delegatestore.OutcomeFailed {
		t.Fatalf("expected failed, got %v", finish.outcome)
	}
	if finish.reason != "missing_terminal" {
		t.Fatalf("expected 'missing_terminal', got %q", finish.reason)
	}
}

func TestDelegatePreparedFinishTerminalErrorNoMetadata(t *testing.T) {
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError}
	finish := delegatePreparedFinish(packet)
	if finish.outcome != delegatestore.OutcomeFailed {
		t.Fatalf("expected failed, got %v", finish.outcome)
	}
	if finish.reason != "terminal_error" {
		t.Fatalf("expected 'terminal_error', got %q", finish.reason)
	}
}

func TestDelegatePreparedFinishWithMetadata(t *testing.T) {
	now := time.Now().UTC()
	metadata := delegateTerminalPacketMetadata{
		RunEndedAt: now.Format(time.RFC3339Nano),
		Outcome:   delegatestore.OutcomeFailed,
		Reason:    "custom failure",
	}
	raw, _ := json.Marshal(metadata)
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Metadata: raw}
	finish := delegatePreparedFinish(packet)
	if finish.outcome != delegatestore.OutcomeFailed {
		t.Fatalf("expected failed, got %v", finish.outcome)
	}
	if finish.reason != "custom failure" {
		t.Fatalf("expected 'custom failure', got %q", finish.reason)
	}
	if !finish.endedAt.Equal(now) {
		t.Fatalf("expected endedAt = %v, got %v", now, finish.endedAt)
	}
}

func TestDelegatePreparedFinishCancelled(t *testing.T) {
	metadata := delegateTerminalPacketMetadata{
		Outcome: delegatestore.OutcomeCancelled,
		Reason:  "cancelled",
	}
	raw, _ := json.Marshal(metadata)
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Metadata: raw}
	finish := delegatePreparedFinish(packet)
	if finish.outcome != delegatestore.OutcomeCancelled {
		t.Fatalf("expected cancelled, got %v", finish.outcome)
	}
	if finish.reason != "cancelled" {
		t.Fatalf("expected 'cancelled', got %q", finish.reason)
	}
}

func TestDelegatePreparedFinishCancelledWrongReason(t *testing.T) {
	metadata := delegateTerminalPacketMetadata{
		Outcome: delegatestore.OutcomeCancelled,
		Reason:  "other_reason",
	}
	raw, _ := json.Marshal(metadata)
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Metadata: raw}
	finish := delegatePreparedFinish(packet)
	if finish.outcome != delegatestore.OutcomeFailed {
		t.Fatalf("expected failed (wrong reason for cancelled), got %v", finish.outcome)
	}
}

func TestDelegatePreparedFinishInvalidMetadata(t *testing.T) {
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Metadata: json.RawMessage("invalid")}
	finish := delegatePreparedFinish(packet)
	if finish.outcome != delegatestore.OutcomeFailed {
		t.Fatalf("expected failed for invalid metadata, got %v", finish.outcome)
	}
	if finish.reason != "terminal_error" {
		t.Fatalf("expected 'terminal_error' default, got %q", finish.reason)
	}
}

// ---------------------------------------------------------------------------
// cloneDelegateTerminalPacket
// ---------------------------------------------------------------------------

func TestCloneDelegateTerminalPacket(t *testing.T) {
	original := delegatestore.TerminalPacket{
		Kind:    delegatestore.PacketTerminalError,
		Message: json.RawMessage(`"original"`),
		Warnings: []string{"w1"},
		Metadata: json.RawMessage(`{"key":"val"}`),
	}
	clone := cloneDelegateTerminalPacket(original)
	if clone.Kind != original.Kind {
		t.Fatalf("kind mismatch")
	}
	// Modify clone
	clone.Message[0] = 'X'
	if original.Message[0] == 'X' {
		t.Fatalf("expected Message to be deep-copied")
	}
	clone.Warnings[0] = "modified"
	if original.Warnings[0] == "modified" {
		t.Fatalf("expected Warnings to be deep-copied")
	}
}

func TestCloneDelegateTerminalPacketWithStructuredResultValid(t *testing.T) {
	valid := true
	original := delegatestore.TerminalPacket{
		StructuredResult:      json.RawMessage(`{}`),
		StructuredResultValid: &valid,
	}
	clone := cloneDelegateTerminalPacket(original)
	if clone.StructuredResultValid == nil || *clone.StructuredResultValid != true {
		t.Fatalf("expected StructuredResultValid to be cloned")
	}
	// Modify clone
	*clone.StructuredResultValid = false
	if *original.StructuredResultValid == false {
		t.Fatalf("expected original to be unaffected")
	}
}

func TestCloneDelegateTerminalPacketNilStructuredResultValid(t *testing.T) {
	original := delegatestore.TerminalPacket{
		StructuredResultValid: nil,
	}
	clone := cloneDelegateTerminalPacket(original)
	if clone.StructuredResultValid != nil {
		t.Fatalf("expected nil preserved")
	}
}

// ---------------------------------------------------------------------------
// delegateRunFinishedEvent
// ---------------------------------------------------------------------------

func TestDelegateRunFinishedEvent(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 5}
	now := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	packet := &delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"err"`)}
	event := delegateRunFinishedEvent(lease, delegatestore.OutcomeFailed, delegatestore.DispositionTerminalError, "test_reason", now, "del_1", packet)
	if event.Kind != delegatestore.EventDelegateRunFinished {
		t.Fatalf("kind = %v", event.Kind)
	}
	if event.DelegateID != "dlg_1" {
		t.Fatalf("delegateID = %q", event.DelegateID)
	}
	if event.RunFinished == nil {
		t.Fatalf("expected RunFinished")
	}
	if event.RunFinished.Generation != 5 {
		t.Fatalf("generation = %d", event.RunFinished.Generation)
	}
	if event.RunFinished.Outcome.Status != delegatestore.OutcomeFailed {
		t.Fatalf("outcome = %v", event.RunFinished.Outcome.Status)
	}
	if event.RunFinished.Outcome.Reason != "test_reason" {
		t.Fatalf("reason = %q", event.RunFinished.Outcome.Reason)
	}
	if !event.RunFinished.Outcome.EndedAt.Equal(now) {
		t.Fatalf("endedAt = %v", event.RunFinished.Outcome.EndedAt)
	}
	if event.RunFinished.Disposition != delegatestore.DispositionTerminalError {
		t.Fatalf("disposition = %v", event.RunFinished.Disposition)
	}
	if event.RunFinished.DeliveryID != "del_1" {
		t.Fatalf("deliveryID = %q", event.RunFinished.DeliveryID)
	}
	if event.RunFinished.Packet == nil {
		t.Fatalf("expected packet")
	}
}

func TestDelegateRunFinishedEventNilPacket(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	event := delegateRunFinishedEvent(lease, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "done", time.Now(), "del_1", nil)
	if event.RunFinished.Packet != nil {
		t.Fatalf("expected nil packet")
	}
}

// ---------------------------------------------------------------------------
// delegateFinishMetadataEvents
// ---------------------------------------------------------------------------

func TestDelegateFinishMetadataEventsNonExhausted(t *testing.T) {
	events := []delegatestore.Event{
		{Kind: delegatestore.EventDelegateRunFinished, RunFinished: &delegatestore.RunFinished{}},
	}
	result := delegateFinishMetadataEvents(events, delegateLease{delegateID: "dlg_1"}, delegateFinish{}, delegatestore.OutcomeCompleted, "test")
	if len(result) != 1 {
		t.Fatalf("expected 1 event (non-exhausted, no change)")
	}
}

func TestDelegateFinishMetadataEventsExhausted(t *testing.T) {
	events := []delegatestore.Event{
		{Kind: delegatestore.EventDelegateRunFinished, RunFinished: &delegatestore.RunFinished{}},
	}
	finish := delegateFinish{
		exhaustionBudget: delegatestore.ExhaustionBudgetTurns,
		exhaustionLimit:  10,
	}
	result := delegateFinishMetadataEvents(events, delegateLease{delegateID: "dlg_1"}, finish, delegatestore.OutcomeExhausted, "test")
	if len(result) != 1 {
		t.Fatalf("expected 1 event")
	}
	if result[0].RunFinished.Outcome.ExhaustionBudget != delegatestore.ExhaustionBudgetTurns {
		t.Fatalf("expected budget set")
	}
	if result[0].RunFinished.Outcome.ExhaustionLimit != 10 {
		t.Fatalf("expected limit set")
	}
}

func TestDelegateFinishMetadataEventsExhaustedNonResumable(t *testing.T) {
	events := []delegatestore.Event{
		{Kind: delegatestore.EventDelegateRunFinished, RunFinished: &delegatestore.RunFinished{}},
	}
	resumable := false
	finish := delegateFinish{exhaustionResumable: &resumable}
	result := delegateFinishMetadataEvents(events, delegateLease{delegateID: "dlg_1"}, finish, delegatestore.OutcomeExhausted, "reason")
	if len(result) != 2 {
		t.Fatalf("expected 2 events (run finished + resumability closed)")
	}
	if result[1].Kind != delegatestore.EventDelegateResumabilityClosed {
		t.Fatalf("expected resumability closed event")
	}
}

func TestDelegateFinishMetadataEventsExhaustedResumable(t *testing.T) {
	events := []delegatestore.Event{
		{Kind: delegatestore.EventDelegateRunFinished, RunFinished: &delegatestore.RunFinished{}},
	}
	resumable := true
	finish := delegateFinish{exhaustionResumable: &resumable}
	result := delegateFinishMetadataEvents(events, delegateLease{delegateID: "dlg_1"}, finish, delegatestore.OutcomeExhausted, "reason")
	if len(result) != 1 {
		t.Fatalf("expected 1 event (resumable, no closure)")
	}
	if result[0].RunFinished.Outcome.Resumable == nil || !*result[0].RunFinished.Outcome.Resumable {
		t.Fatalf("expected resumable=true set on outcome")
	}
}

func TestDelegateFinishMetadataEventsNoRunFinished(t *testing.T) {
	events := []delegatestore.Event{
		{Kind: delegatestore.EventDelegateResumabilityClosed},
	}
	result := delegateFinishMetadataEvents(events, delegateLease{delegateID: "dlg_1"}, delegateFinish{}, delegatestore.OutcomeExhausted, "reason")
	if len(result) != 1 {
		t.Fatalf("expected 1 event unchanged (no RunFinished)")
	}
}

// ---------------------------------------------------------------------------
// hasSettlementClaimLocked
// ---------------------------------------------------------------------------

func TestHasSettlementClaimLockedEmpty(t *testing.T) {
	c := &delegateTreeController{settlementClaims: map[uint64]*delegateSettlementClaim{}}
	if c.hasSettlementClaimLocked(delegateLease{delegateID: "dlg_1", generation: 1}) {
		t.Fatalf("expected false for empty claims")
	}
}

func TestHasSettlementClaimLockedFound(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{
			1: {lease: lease},
		},
	}
	if !c.hasSettlementClaimLocked(lease) {
		t.Fatalf("expected true for found claim")
	}
}

func TestHasSettlementClaimLockedDifferentGeneration(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{
			1: {lease: delegateLease{delegateID: "dlg_1", generation: 1}},
		},
	}
	if c.hasSettlementClaimLocked(delegateLease{delegateID: "dlg_1", generation: 2}) {
		t.Fatalf("expected false for different generation")
	}
}

func TestHasSettlementClaimLockedNilClaim(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: nil},
	}
	if c.hasSettlementClaimLocked(delegateLease{delegateID: "dlg_1", generation: 1}) {
		t.Fatalf("expected false for nil claim")
	}
}

// ---------------------------------------------------------------------------
// hasSteeringClaimLocked
// ---------------------------------------------------------------------------

func TestHasSteeringClaimLockedEmpty(t *testing.T) {
	c := &delegateTreeController{steeringClaims: map[uint64]*delegateSteeringClaim{}}
	if c.hasSteeringClaimLocked(delegateLease{delegateID: "dlg_1", generation: 1}) {
		t.Fatalf("expected false for empty claims")
	}
}

func TestHasSteeringClaimLockedFound(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		steeringClaims: map[uint64]*delegateSteeringClaim{
			1: {lease: lease},
		},
	}
	if !c.hasSteeringClaimLocked(lease) {
		t.Fatalf("expected true for found claim")
	}
}

func TestHasSteeringClaimLockedNilClaim(t *testing.T) {
	c := &delegateTreeController{
		steeringClaims: map[uint64]*delegateSteeringClaim{1: nil},
	}
	if c.hasSteeringClaimLocked(delegateLease{delegateID: "dlg_1", generation: 1}) {
		t.Fatalf("expected false for nil claim")
	}
}

// ---------------------------------------------------------------------------
// releaseSettlementClaimLocked
// ---------------------------------------------------------------------------

func TestReleaseSettlementClaimLocked(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: {}},
	}
	c.releaseSettlementClaimLocked(1)
	if _, exists := c.settlementClaims[1]; exists {
		t.Fatalf("expected claim to be deleted")
	}
}

func TestReleaseSettlementClaimLockedWithStop(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{1: {}},
		stop: &delegateStopState{settlementClaims: map[uint64]struct{}{1: {}}},
	}
	c.releaseSettlementClaimLocked(1)
	if _, exists := c.stop.settlementClaims[1]; exists {
		t.Fatalf("expected claim to be deleted from stop")
	}
}

func TestReleaseSettlementClaimLockedNonExistent(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{},
	}
	c.releaseSettlementClaimLocked(999) // should be a no-op
}

// ---------------------------------------------------------------------------
// releaseSettlementClaimsForLeaseLocked
// ---------------------------------------------------------------------------

func TestReleaseSettlementClaimsForLeaseLocked(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{
			1: {lease: lease},
			2: {lease: delegateLease{delegateID: "dlg_2", generation: 1}},
		},
	}
	c.releaseSettlementClaimsForLeaseLocked(lease)
	if _, exists := c.settlementClaims[1]; exists {
		t.Fatalf("expected claim 1 to be deleted")
	}
	if _, exists := c.settlementClaims[2]; !exists {
		t.Fatalf("expected claim 2 to remain")
	}
}
