package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
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

// A deliverer that claimed the first frame and then exits early, because the
// frame stopped being the current pending one, releases the receipt with its
// claim: the coalescing commit left the receipt to it.
func TestSupersededStableWatchReceiptReleasesWithEarlyExitClaim(t *testing.T) {
	fixture := newCommunicateStableWatch(t)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if fixture.sourceJM.isCurrentPendingWatchSend(fixture.onlyWatchConfig(t), first) {
		t.Fatal("first frame still current after the coalesce")
	}
	fixture.sourceJM.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Equal(got, []string{second.DeliveryID}) {
		t.Fatalf("delivery receipts = %v, want only %s", got, second.DeliveryID)
	}
}

// When the replacement is delivered before the superseded deliverer reaches
// its durable check, there is no pending frame left for the key at all. The
// superseded deliverer still stands down without a staleness error, and its
// receipt goes with its claim, leaving none behind.
func TestSupersededStableWatchDelivererAfterReplacementDelivered(t *testing.T) {
	fixture := newCommunicateStableWatch(t)
	cfg := fixture.onlyWatchConfig(t)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !fixture.sourceJM.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "second"})
	second := fixture.requireOnePending(t).state
	if delivered, err := fixture.sourceJM.deliverPendingWatchSend(cfg, second, false); !delivered || err != nil {
		t.Fatalf("deliver replacement: delivered=%t err=%v", delivered, err)
	}
	delivered, err := fixture.sourceJM.deliverStableWatchSend(cfg, first)
	fixture.sourceJM.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if delivered || err != nil {
		t.Fatalf("superseded deliverer: delivered=%t err=%v, want a quiet stand-down", delivered, err)
	}
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); len(got) != 0 {
		t.Fatalf("delivery receipts outlived their frames: %v", got)
	}
}

// The replacement frame is durable before its runtime commit marks the
// claimed first frame superseded. A deliverer of the first frame that reaches
// its durable check in that window finds a newer pending frame for its key,
// stands down without a staleness error, and its receipt still goes once the
// coalesce and its claim have both finished.
func TestSupersededStableWatchDelivererBeforeRuntimeCommit(t *testing.T) {
	fixture := newCommunicateStableWatch(t)
	cfg := fixture.onlyWatchConfig(t)
	jm := fixture.sourceJM
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "first"})
	first := fixture.requireOnePending(t).state
	if !jm.claimWatchSendDelivery(first.DeliveryID) {
		t.Fatal("claim first frame")
	}
	var standDown error
	ranInWindow := false
	originalAppend := jm.appendEvent
	jm.appendEvent = func(event jobstore.Event) error {
		if err := originalAppend(event); err != nil {
			return err
		}
		if !ranInWindow && event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.DeliveryID != first.DeliveryID {
			ranInWindow = true
			var delivered bool
			delivered, standDown = jm.deliverStableWatchSend(cfg, first)
			if delivered {
				standDown = errors.New("superseded frame was delivered")
			}
		}
		return nil
	}
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "second"})
	jm.appendEvent = originalAppend
	jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
	if !ranInWindow {
		t.Fatal("the replacement's pending append never ran")
	}
	if standDown != nil {
		t.Fatalf("deliverer in the commit window: %v, want a quiet stand-down", standDown)
	}
	second := fixture.requireOnePending(t).state
	if got := stableWatchDeliveryReceiptIDs(fixture.controller); !slices.Equal(got, []string{second.DeliveryID}) {
		t.Fatalf("delivery receipts = %v, want only %s", got, second.DeliveryID)
	}
}
