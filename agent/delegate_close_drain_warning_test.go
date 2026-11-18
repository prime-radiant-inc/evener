package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A session close that abandons an inline delivery -- its owning tool commit is
// gone and no owner can still answer it -- must record the lost result. That
// notice is the one a closing session most needs, because the result is gone and
// the session is about to stop being where its owner would look (#6308).
//
// The close-driven drain (drainStopForClose) runs the same reconciliation as the
// stop driver and used to hand it a nil root, which dropped every plan the pass
// settled, warnings included. It now passes the controller's root, the receiver
// the stop driver already uses. The root outlives the drain: bootstrap attaches
// it once and never clears it, and Session.closeTerminal closes the root's event
// stream only after closeOwnedDelegateRuntimeTree has returned.
//
// The test drives drainStopForClose itself rather than closeRuntimeTree so it
// observes exactly the root this drain publishes to; the surrounding close path
// is what calls it (closeRuntimeTree, and joinOrDrainStopForClose's fallback).
func TestDelegateCloseDrainReportsLostInlineResult(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	f.root.events = make(chan events.SessionEvent, 4)
	if err := f.controller.drainStopForClose(tripwireCtx(t), f.controller.stopForResult(f.stop)); err != nil {
		t.Fatalf("close-driven drain with a lost inline commit = %v, want the abandoned receipt to settle the stop", err)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("the close-driven drain did not complete the stop after abandoning the inline receipt")
	}
	f.controller.mu.Lock()
	receipts := len(f.controller.deliveries)
	f.controller.mu.Unlock()
	if receipts != 0 {
		t.Fatalf("delivery admissions after the drain abandoned the inline receipt = %d, want 0", receipts)
	}
	stored, err := delegatestore.ReadEvents(f.storePath)
	if err != nil {
		t.Fatalf("read delegate events: %v", err)
	}
	// The result was lost, not delivered: the drain must not leave a durable
	// acknowledgment behind for the abandoned delivery.
	if delegateEventsAcknowledgeDelivery(stored, f.deliveries[0].deliveryID) {
		t.Fatalf("the drain acknowledged the abandoned inline delivery %q", f.deliveries[0].deliveryID)
	}
	reported := 0
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("lost-result warnings recorded on the closing root = %d, want exactly 1", reported)
	}
}
