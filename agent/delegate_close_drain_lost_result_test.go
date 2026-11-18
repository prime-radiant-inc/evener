package agent

import (
	"context"
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
