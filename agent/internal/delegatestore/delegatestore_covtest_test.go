package delegatestore

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func covPendingDelivery(id string, generation uint64, owner string, kind PacketKind, marker string) PendingDelivery {
	valid := kind == PacketReported
	return PendingDelivery{
		DeliveryID:      id,
		Generation:      generation,
		OwnerDelegateID: owner,
		Packet: TerminalPacket{
			Kind:                   kind,
			Message:                json.RawMessage(`{"message":"` + marker + `"}`),
			StructuredResult:       json.RawMessage(`{"result":"` + marker + `"}`),
			StructuredResultValid:  &valid,
			StructuredResultReason: "reason-" + marker,
			Warnings:               []string{"warning-" + marker, "second-warning-" + marker},
			Metadata:               json.RawMessage(`{"metadata":"` + marker + `"}`),
		},
	}
}

func TestCovApplyDeliveryAcknowledged(t *testing.T) {
	state := State{}

	err := applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err == nil || !strings.Contains(err.Error(), `delegate "dlg_1" does not exist`) {
		t.Fatalf("missing aggregate error = %v", err)
	}

	state["dlg_1"] = &Aggregate{DelegateID: "dlg_1"}
	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: ""},
	})
	if err == nil || !strings.Contains(err.Error(), "delivery acknowledgement id is empty") {
		t.Fatalf("expected empty delivery id error, got %v", err)
	}

	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "not the pending head") {
		t.Fatalf("expected not-pending-head error, got %v", err)
	}

	state["dlg_1"].PendingDeliveries = []PendingDelivery{
		covPendingDelivery("del_other", 11, "dlg_owner_head", PacketReported, "rejected-head"),
		covPendingDelivery("del_tail", 22, "dlg_owner_tail", PacketTerminalError, "rejected-tail"),
	}
	wantRejected := []PendingDelivery{
		covPendingDelivery("del_other", 11, "dlg_owner_head", PacketReported, "rejected-head"),
		covPendingDelivery("del_tail", 22, "dlg_owner_tail", PacketTerminalError, "rejected-tail"),
	}
	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "not the pending head") {
		t.Fatalf("expected not-pending-head error, got %v", err)
	}
	if !reflect.DeepEqual(state["dlg_1"].PendingDeliveries, wantRejected) {
		t.Fatalf("mismatched acknowledgement mutated pending deliveries: got %+v, want %+v", state["dlg_1"].PendingDeliveries, wantRejected)
	}

	state["dlg_1"].PendingDeliveries = []PendingDelivery{
		covPendingDelivery("del_1", 31, "dlg_success_head", PacketReported, "success-head"),
		covPendingDelivery("del_2", 42, "dlg_success_tail", PacketTerminalError, "success-tail"),
	}
	wantTail := covPendingDelivery("del_2", 42, "dlg_success_tail", PacketTerminalError, "success-tail")
	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []PendingDelivery{wantTail}
	if !reflect.DeepEqual(state["dlg_1"].PendingDeliveries, want) {
		t.Fatalf("after acknowledgement: pending = %+v, want %+v", state["dlg_1"].PendingDeliveries, want)
	}
}

func TestCovApplyAttentionChanged(t *testing.T) {
	state := State{}

	err := applyAttentionChanged(state, Event{
		DelegateID:       "dlg_1",
		AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true},
	})
	if err == nil || !strings.Contains(err.Error(), `delegate "dlg_1" does not exist`) {
		t.Fatalf("missing aggregate error = %v", err)
	}

	state["dlg_1"] = &Aggregate{DelegateID: "dlg_1", NeedsAttention: false}
	err = applyAttentionChanged(state, Event{
		DelegateID:       "dlg_1",
		AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state["dlg_1"].NeedsAttention {
		t.Fatal("NeedsAttention should be true")
	}

	err = applyAttentionChanged(state, Event{
		DelegateID:       "dlg_1",
		AttentionChanged: &DelegateAttentionChanged{NeedsAttention: false},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state["dlg_1"].NeedsAttention {
		t.Fatal("NeedsAttention should be false")
	}
}
