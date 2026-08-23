package delegatestore

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateTerminalPacket covers validateTerminalPacket (fold.go:491-508)
// for all branches: invalid kind, invalid message, invalid structured result,
// oversized structured result, invalid metadata, and valid packets.
func TestValidateTerminalPacket(t *testing.T) {
	t.Parallel()
	// Invalid kind.
	if err := validateTerminalPacket(TerminalPacket{Kind: "bad", Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("invalid kind should error")
	}
	// Missing message.
	if err := validateTerminalPacket(TerminalPacket{Kind: PacketReported}); err == nil {
		t.Fatal("missing message should error")
	}
	// Invalid message JSON.
	if err := validateTerminalPacket(TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`not json`)}); err == nil {
		t.Fatal("invalid message JSON should error")
	}
	// Invalid structured result JSON.
	if err := validateTerminalPacket(TerminalPacket{
		Kind:             PacketReported,
		Message:          json.RawMessage(`"ok"`),
		StructuredResult: json.RawMessage(`not json`),
	}); err == nil {
		t.Fatal("invalid structured result JSON should error")
	}
	// Oversized structured result.
	large := strings.Repeat("x", MaxTerminalStructuredResultBytes+1)
	if err := validateTerminalPacket(TerminalPacket{
		Kind:             PacketReported,
		Message:          json.RawMessage(`"ok"`),
		StructuredResult: json.RawMessage(`"` + large + `"`),
	}); err == nil {
		t.Fatal("oversized structured result should error")
	}
	// Invalid metadata JSON.
	if err := validateTerminalPacket(TerminalPacket{
		Kind:     PacketReported,
		Message:  json.RawMessage(`"ok"`),
		Metadata: json.RawMessage(`not json`),
	}); err == nil {
		t.Fatal("invalid metadata JSON should error")
	}
	// Valid reported packet.
	if err := validateTerminalPacket(TerminalPacket{
		Kind:    PacketReported,
		Message: json.RawMessage(`"result"`),
	}); err != nil {
		t.Fatalf("valid reported: %v", err)
	}
	// Valid terminal error packet.
	if err := validateTerminalPacket(TerminalPacket{
		Kind:    PacketTerminalError,
		Message: json.RawMessage(`"error message"`),
	}); err != nil {
		t.Fatalf("valid terminal error: %v", err)
	}
	// Valid with all optional fields.
	if err := validateTerminalPacket(TerminalPacket{
		Kind:             PacketReported,
		Message:          json.RawMessage(`"result"`),
		StructuredResult: json.RawMessage(`{"ok":true}`),
		Metadata:         json.RawMessage(`{"key":"value"}`),
	}); err != nil {
		t.Fatalf("valid with options: %v", err)
	}
}

// TestValidateFinishPacket covers validateFinishPacket (fold.go:510-544)
// for the error paths not covered by existing integration tests.
func TestValidateFinishPacket(t *testing.T) {
	t.Parallel()
	aggregate := &Aggregate{DelegateID: "dlg_1", Trigger: TriggerAttention}

	// completed_no_action with wrong trigger.
	aggNonAttention := &Aggregate{DelegateID: "dlg_1", Trigger: TriggerInitial}
	if err := validateFinishPacket(aggNonAttention, &RunFinished{Disposition: DispositionCompletedNoAction, Outcome: Outcome{Status: OutcomeCompleted}}, nil); err == nil {
		t.Fatal("completed_no_action with non-attention trigger should error")
	}
	// completed_no_action with wrong outcome.
	if err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionCompletedNoAction, Outcome: Outcome{Status: OutcomeFailed}}, nil); err == nil {
		t.Fatal("completed_no_action with non-completed outcome should error")
	}
	// completed_no_action with delivery ID.
	if err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionCompletedNoAction, Outcome: Outcome{Status: OutcomeCompleted}, DeliveryID: "del_1"}, nil); err == nil {
		t.Fatal("completed_no_action with delivery ID should error")
	}
	// completed_no_action valid.
	if err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionCompletedNoAction, Outcome: Outcome{Status: OutcomeCompleted}}, nil); err != nil {
		t.Fatalf("valid completed_no_action: %v", err)
	}

	// Non-completed_no_action with nil packet -> error.
	if err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionReported}, nil); err == nil {
		t.Fatal("nil packet with reported disposition should error")
	}

	// Reported disposition with terminal-error packet kind -> error.
	if err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionReported, DeliveryID: "del"}, &TerminalPacket{Kind: PacketTerminalError, Message: json.RawMessage(`"err"`)}); err == nil {
		t.Fatal("reported with terminal-error packet should error")
	}
	// Terminal-error disposition with reported packet kind -> error.
	if err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionTerminalError, DeliveryID: "del"}, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("terminal-error with reported packet should error")
	}

	// Empty delivery ID (non-observer-callback) -> error.
	if err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionReported, DeliveryID: ""}, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("empty delivery ID should error")
	}

	// Valid reported with delivery ID.
	if err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionReported, DeliveryID: "del_1"}, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err != nil {
		t.Fatalf("valid reported: %v", err)
	}
}

// TestValidateFinishPacketObserverCallback covers the observer-callback path
// (fold.go:535-539).
func TestValidateFinishPacketObserverCallback(t *testing.T) {
	t.Parallel()
	aggregate := &Aggregate{
		DelegateID:       "dlg_1",
		Trigger:          TriggerAttention,
		PreparedTerminal: &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)},
	}
	// Valid observer callback — pass a non-nil packet (the function parameter)
	// even though finish.Packet is nil.
	if err := validateFinishPacket(aggregate, &RunFinished{
		Disposition:               DispositionReported,
		ObserverCallbackDelivered: true,
	}, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err != nil {
		t.Fatalf("valid observer callback: %v", err)
	}
	// Observer callback with wrong trigger.
	aggBad := &Aggregate{DelegateID: "dlg_1", Trigger: TriggerInitial, PreparedTerminal: &TerminalPacket{}}
	if err := validateFinishPacket(aggBad, &RunFinished{Disposition: DispositionReported, ObserverCallbackDelivered: true}, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("observer callback with wrong trigger should error")
	}
	// Observer callback without prepared terminal.
	aggNoPrep := &Aggregate{DelegateID: "dlg_1", Trigger: TriggerAttention}
	if err := validateFinishPacket(aggNoPrep, &RunFinished{Disposition: DispositionReported, ObserverCallbackDelivered: true}, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("observer callback without prepared terminal should error")
	}
	// Observer callback with delivery ID.
	if err := validateFinishPacket(aggregate, &RunFinished{
		Disposition:               DispositionReported,
		ObserverCallbackDelivered: true,
		DeliveryID:                "del_1",
	}, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("observer callback with delivery ID should error")
	}
}

// TestAppendDelivery covers appendDelivery (fold.go:547-568) for the error
// paths: empty delivery ID, nil packet, wrong delivery ID format, duplicate.
func TestAppendDelivery(t *testing.T) {
	t.Parallel()
	aggregate := &Aggregate{DelegateID: "dlg_1", Generation: 1}

	// Empty delivery ID -> error.
	if err := appendDelivery(aggregate, "", 1, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("empty delivery ID should error")
	}
	// Nil packet -> error.
	if err := appendDelivery(aggregate, "del_1", 1, nil); err == nil {
		t.Fatal("nil packet should error")
	}
	// Wrong delivery ID format.
	if err := appendDelivery(aggregate, "wrong_id", 1, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("wrong delivery ID format should error")
	}
	// Valid delivery.
	wantID := "dlg_1/delivery/1"
	if err := appendDelivery(aggregate, wantID, 1, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err != nil {
		t.Fatalf("valid delivery: %v", err)
	}
	if len(aggregate.PendingDeliveries) != 1 || aggregate.PendingDeliveries[0].DeliveryID != wantID {
		t.Fatalf("pending deliveries = %+v", aggregate.PendingDeliveries)
	}
	// Duplicate delivery -> error.
	if err := appendDelivery(aggregate, wantID, 1, &TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"ok"`)}); err == nil {
		t.Fatal("duplicate delivery should error")
	}
}
