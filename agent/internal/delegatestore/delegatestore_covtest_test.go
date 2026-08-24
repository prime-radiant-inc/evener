package delegatestore

import (
	"reflect"
	"strings"
	"testing"
)

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

	pending := []PendingDelivery{{DeliveryID: "del_other"}}
	state["dlg_1"].PendingDeliveries = append([]PendingDelivery(nil), pending...)
	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "not the pending head") {
		t.Fatalf("expected not-pending-head error, got %v", err)
	}
	if !reflect.DeepEqual(state["dlg_1"].PendingDeliveries, pending) {
		t.Fatalf("mismatched acknowledgement mutated pending deliveries: %+v", state["dlg_1"].PendingDeliveries)
	}

	state["dlg_1"].PendingDeliveries = []PendingDelivery{
		{DeliveryID: "del_1"},
		{DeliveryID: "del_2"},
	}
	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []PendingDelivery{{DeliveryID: "del_2"}}
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
