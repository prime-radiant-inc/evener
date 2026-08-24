package delegatestore

import (
	"strings"
	"testing"
)

// TestCovApplyDeliveryAcknowledged covers applyDeliveryAcknowledged
// (fold.go lines 341-355).
func TestCovApplyDeliveryAcknowledged(t *testing.T) {
	state := State{}

	// Missing aggregate (no delegate registered).
	err := applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err == nil {
		t.Fatal("expected error for missing aggregate")
	}

	// Register a delegate and test with empty delivery ID.
	state["dlg_1"] = &Aggregate{DelegateID: "dlg_1"}
	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: ""},
	})
	if err == nil || !strings.Contains(err.Error(), "delivery acknowledgement id is empty") {
		t.Fatalf("expected empty delivery id error, got %v", err)
	}

	// No pending deliveries.
	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "not the pending head") {
		t.Fatalf("expected not-pending-head error, got %v", err)
	}

	// Delivery ID doesn't match pending head.
	state["dlg_1"].PendingDeliveries = []PendingDelivery{
		{DeliveryID: "del_other"},
	}
	err = applyDeliveryAcknowledged(state, Event{
		DelegateID:           "dlg_1",
		DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "del_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "not the pending head") {
		t.Fatalf("expected not-pending-head error, got %v", err)
	}

	// Successful acknowledgement — removes head.
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
	if len(state["dlg_1"].PendingDeliveries) != 1 || state["dlg_1"].PendingDeliveries[0].DeliveryID != "del_2" {
		t.Fatalf("after ack: pending = %+v", state["dlg_1"].PendingDeliveries)
	}
}

// TestCovApplyAttentionChanged covers applyAttentionChanged
// (fold.go lines 357-364).
func TestCovApplyAttentionChanged(t *testing.T) {
	state := State{}

	// Missing aggregate.
	err := applyAttentionChanged(state, Event{
		DelegateID:       "dlg_1",
		AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true},
	})
	if err == nil {
		t.Fatal("expected error for missing aggregate")
	}

	// Set attention.
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

	// Clear attention.
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
