package agent

import (
	"context"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// stableWatchDeliveryReceiptIDs lists the delivery ids the controller still
// holds a delivery receipt for. A receipt fences a delegate stop until it
// completes, so one that outlives its frame stalls closing the tree.
func stableWatchDeliveryReceiptIDs(c *delegateTreeController) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, receipt := range c.watchDeliveries {
		ids = append(ids, receipt.deliveryID)
	}
	slices.Sort(ids)
	return ids
}

// newCommunicateStableWatch watches dlg_source's communicate events from the
// root, so each event records a pending stable-receiver frame.
func newCommunicateStableWatch(t *testing.T) *stableWatchRuntimeFixture {
	t.Helper()
	fixture := newStableWatchRuntimeBase(t, nil)
	if _, err := jobWatchToolWithContext(context.Background(), fixture.root, map[string]any{
		"operation": "create",
		"source":    "dlg_source",
		"events":    []any{"communicate"},
	}, 4096); err != nil {
		t.Fatalf("create watch: %v", err)
	}
	return fixture
}

// When a second frame coalesces over a stable-receiver frame that is still
// pending, the superseded frame's delivery receipt must be released with it:
// only the frame that will be delivered keeps one. Otherwise the orphaned
// receipt fences the receiver's stop forever, and closing the root waits out
// the close budget.
func TestCoalescedStableWatchFrameReleasesSupersededReceipt(t *testing.T) {
	fixture := newCommunicateStableWatch(t)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if second.DeliveryID == first.DeliveryID {
		t.Fatalf("coalesced frame kept delivery id %s", first.DeliveryID)
	}
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Equal(got, []string{second.DeliveryID}) {
		t.Fatalf("delivery receipts = %v, want only the pending frame's %s (superseded %s)", got, second.DeliveryID, first.DeliveryID)
	}
}

// A deliverer already handing off the first frame when the second coalesces
// over it keeps the first receipt through the supersede, then stands down
// without an error and releases it: the newer frame carries the content on.
func TestInFlightSupersededStableWatchFrameStandsDown(t *testing.T) {
	fixture := newCommunicateStableWatch(t)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); len(got) != 2 {
		t.Fatalf("the in-flight first frame lost its receipt at the supersede: %v", got)
	}
	delivered, err := fixture.sourceJM.deliverStableWatchSend(fixture.onlyWatchConfig(t), first)
	fixture.sourceJM.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if delivered || err != nil {
		t.Fatalf("superseded deliverer: delivered=%t err=%v, want a quiet stand-down", delivered, err)
	}
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Equal(got, []string{second.DeliveryID}) {
		t.Fatalf("delivery receipts = %v, want only %s", got, second.DeliveryID)
	}
}
