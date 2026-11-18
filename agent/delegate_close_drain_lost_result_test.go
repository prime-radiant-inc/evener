package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// The close path runs the same reconciliation as the stop driver, so an inline
// receipt whose owning commit is gone is abandoned and its lost result recorded.
// That result must reach the root: the session is closing, so it is the last
// moment its owner can be told.
//
// Two contracts are pinned here together, because the second is only safe
// because of the first:
//
//   - The close drain does NOT publish the warning itself. Publishing can block
//     on a stalled authoritative event consumer, so doing it inside the stop
//     loop would let an early subtree spend the budget a later subtree needs to
//     reach its own durable stop.
//   - The close's own publication point then publishes it exactly once, so the
//     deferral costs the reader nothing.
//
// This drives drainStopForClose, not drainStop. The drain-level pins call
// drainStop with a root they supply themselves, so they exercise the abandon
// branch and can never observe what the close path does with the result.
func TestDelegateStopCloseDrainReportsLostInlineReceipt(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	if f.root == nil {
		t.Fatal("fixture did not create the root runtime")
	}
	f.root.events = make(chan events.SessionEvent, 8)
	if err := f.controller.drainStopForClose(context.Background(), f.controller.stopForResult(f.stop)); err != nil {
		t.Fatalf("close drain with a lost inline commit = %v, want the abandoned receipt to settle the stop", err)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("close drain did not complete the stop after abandoning the inline receipt")
	}
	f.controller.mu.Lock()
	tracked := len(f.controller.deliveries)
	f.controller.mu.Unlock()
	if tracked != 0 {
		t.Fatalf("delivery admissions after the close drain = %d, want the abandoned receipt released", tracked)
	}
	// The drain must have held the notice rather than emitted it: emitting here
	// is what spent a later subtree's stop budget.
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost {
			t.Fatal("the close drain published the lost-result warning inside the stop loop, where backpressure can spend a later subtree's stop budget")
		}
	}

	// The close's publication point, after every stop is admitted.
	f.controller.publishCloseEvidence()
	found := 0
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("lost-result warnings published by the close = %d, want exactly one: the abandoned receipt's result must reach the root, and only once", found)
	}
}

// The warning reaches the root because the terminal close publishes it, not
// because a test called the publisher. This seeds one stranded inline receipt
// whose owning tool commit is gone into a real session's own controller, drives
// Session.Close, and reads the root's event stream.
//
// Deleting the close's publication call is what this exists to catch: the close
// would still abandon the receipt and settle the stop, so every other assertion
// in this file stays green while the reader is never told the result is gone.
func TestDelegateClosePublishesLostResultFromTerminalClose(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter("WORKER-BRIEF"), nil)
	controller := root.delegateController
	if controller == nil {
		t.Fatal("root has no delegate controller")
	}
	seedStrandedInlineReceiptForClose(t, controller, root)

	var (
		mu       sync.Mutex
		warnings []events.WarningData
	)
	drained := make(chan struct{})
	root.ConsumeEventsLossless(func(ev events.SessionEvent) {
		warning, ok := ev.Data.(events.WarningData)
		if !ok || ev.Kind != events.EventWarning {
			return
		}
		mu.Lock()
		warnings = append(warnings, warning)
		mu.Unlock()
	}, func() { close(drained) })

	root.Close()
	<-drained

	mu.Lock()
	defer mu.Unlock()
	found := 0
	for _, warning := range warnings {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_close_lost" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("lost-result warnings on the root stream after the terminal close = %d, want exactly one", found)
	}
	// The warning above survives because the close took the pass sink and the
	// publisher that drained it is the close's own. Losing that claim would put
	// publication back on whichever goroutine happened to find the warning.
	controller.mu.Lock()
	claimsSink := controller.closeOwnsPassWarnings
	controller.mu.Unlock()
	if !claimsSink {
		t.Fatal("the terminal close never took ownership of the pass sink")
	}
}

// A stop driver the terminal close joins must not publish the warning its pass
// discovers. Publication there runs on the goroutine the close's stop join
// waits for, so `root.emit` is what decides the outcome, and neither branch is
// acceptable: with an authoritative consumer and a full buffer the send parks
// and can outlast the join's deadline, ending the close before the remaining
// subtrees are stopped, and with no consumer reading it drops the notice. The
// pass must leave the warning in the sink, where the close's one publisher
// still has it.
//
// Publication is what this pins, not its downstream consequence: the drain runs
// through the same closeDrain=false branch runStopReconcileDriver uses, with the
// close already holding the sink, and the root stream must still be empty of the
// warning when the drain returns.
func TestDelegateJoinedStopDriverLeavesLostResultForClosePublisher(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter("WORKER-BRIEF"), nil)
	controller := root.delegateController
	if controller == nil {
		t.Fatal("root has no delegate controller")
	}
	seedStrandedInlineReceiptForClose(t, controller, root)

	result, cancelPlan, _, err := controller.StopSubtree(rootDelegateActor(root.ID()), "dlg_close_lost")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	stop := controller.stopForResult(result)
	if stop == nil {
		t.Fatal("no stop state after StopSubtree")
	}

	// The close takes the sink before it joins anything, which is what the
	// terminal close's fence does.
	controller.mu.Lock()
	controller.closeOwnsPassWarnings = true
	controller.mu.Unlock()

	if drainErr := controller.drainStopAbandonable(context.Background(), stop, root, nil, false); drainErr != nil {
		t.Fatalf("joined driver drain = %v, want the abandoned receipt to settle the stop", drainErr)
	}
	if !channelClosed(stop.done) {
		t.Fatal("the joined driver did not settle the stop")
	}

	// Nothing has published yet: the warning is still in the sink, and the
	// stream does not carry it.
	for _, warning := range drainSessionWarnings(root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_close_lost" {
			t.Fatal("the joined stop driver published the lost-result warning on the goroutine the close joins: a parked emit there outlasts the stop-join deadline, and an unread buffer drops it")
		}
	}
	controller.mu.Lock()
	queued := len(controller.passWarnings)
	controller.mu.Unlock()
	if queued != 1 {
		t.Fatalf("warnings left in the pass sink by the joined driver = %d, want 1", queued)
	}

	controller.publishCloseEvidence()
	found := 0
	for _, warning := range drainSessionWarnings(root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_close_lost" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("lost-result warnings published by the close after a joined driver pass = %d, want exactly one", found)
	}
}

// Retirement is not a terminal close, so it closes the delegate store without
// publishing close evidence. The distinction is not cosmetic: publication
// republishes one snapshot per retained delegate, and the daemon bridge resamples
// the whole roster for each, so a retirement that published would do quadratic
// work in the number of delegates the session still holds.
//
// The observable that separates the two policies is the store-only close
// republishing nothing: it must emit no snapshot for the delegates it retains,
// and it must leave a warning already in the pass sink queued for the terminal
// close's publisher.
func TestDelegateStoreOnlyClosePublishesNothing(t *testing.T) {
	controller, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerIdle(t, controller, "dlg_retained", "")
	controller.mu.Lock()
	controller.noteDelegateWarningLocked(events.WarningData{
		Message:    "queued for the terminal close",
		Source:     "delegate",
		Code:       events.WarningCodeDelegateDeliveryResultLost,
		DelegateID: "dlg_retained",
	})
	queuedBefore := len(controller.passWarnings)
	// Count the snapshots the close path emits, rather than sampling a buffer:
	// an empty buffer reads the same before and after, so it cannot tell a
	// publication from a missing one.
	emitted := new(int32)
	inner := controller.emitUpdate
	controller.emitUpdate = func(plan delegateUpdatePlan) {
		atomic.AddInt32(emitted, int32(len(plan.rows)))
		if inner != nil {
			inner(plan)
		}
	}
	innerBatched := controller.emitUpdatesBatched
	controller.emitUpdatesBatched = func(plans delegateMutationPlans) {
		for _, plan := range plans.updates {
			atomic.AddInt32(emitted, int32(len(plan.rows)))
		}
		if innerBatched != nil {
			innerBatched(plans)
		}
	}
	controller.mu.Unlock()
	if queuedBefore == 0 {
		t.Fatal("the pass sink did not hold the warning this test needs")
	}

	if err := controller.closeStoreAfterStopReconcileDriver(context.Background()); err != nil {
		t.Fatalf("store-only close = %v, want it to close the store", err)
	}

	if got := atomic.LoadInt32(emitted); got != 0 {
		t.Fatalf("delegate snapshots emitted by the store-only close = %d, want 0: retirement must not republish the retained roster", got)
	}
	controller.mu.Lock()
	queuedAfter := len(controller.passWarnings)
	published := controller.closeEvidencePublished
	controller.mu.Unlock()
	if queuedAfter != queuedBefore {
		t.Fatalf("warnings left in the pass sink after retirement = %d, want %d: retirement must not publish them", queuedAfter, queuedBefore)
	}
	if published {
		t.Fatal("retirement published close evidence")
	}
}

// seedStrandedInlineReceiptForClose builds the state a closing session has to
// reconcile: one running delegate with an update delivery whose inline receipt
// has no owner left to answer it, so the close's drain must abandon it and
// report the result lost.
func seedStrandedInlineReceiptForClose(t *testing.T, controller *delegateTreeController, root *Session) {
	t.Helper()
	const (
		delegateID = "dlg_close_lost"
		// The owner is deliberately never seeded. A receipt whose owner has
		// neither a live runtime nor a cold-capable idle row has nobody left
		// who could complete it, which is the stranded shape: the reply's tool
		// round is gone and no successor will ever acknowledge it.
		ownerID = "dlg_close_owner"
	)
	// An idle target has no active lease for the stop to wait on, so the drain
	// reaches the delivery acknowledgment pass on its first iteration.
	seedDelegateControllerIdle(t, controller, delegateID, "")
	seedDelegateControllerDelivery(t, controller, delegateID)

	controller.mu.Lock()
	aggregate := controller.durable[delegateID]
	if aggregate == nil || len(aggregate.PendingDeliveries) != 1 {
		controller.mu.Unlock()
		t.Fatalf("delegate %q has no single pending delivery: %#v", delegateID, aggregate)
	}
	head := aggregate.PendingDeliveries[0]
	controller.nextToken++
	token := delegateDeliveryToken{processID: controller.nextToken, deliveryID: head.DeliveryID}
	controller.deliveries[token.processID] = &delegateDeliveryAdmission{
		token:      token,
		delegateID: delegateID,
		ownerID:    ownerID,
		claim:      delegateDeliveryClaimToken{processID: token.processID, deliveryID: head.DeliveryID},
		// inline without retryable is the shape whose owning tool commit is
		// gone: the reply never queued a commit, so nothing can complete it.
		inline: true,
	}
	controller.mu.Unlock()
}
