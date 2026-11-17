package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// The close path runs the same reconciliation as the stop driver, so it must
// hand its plans to the root runtime too. An inline receipt whose owning commit
// is gone is abandoned and its lost result recorded; while the close drain
// passed the root, that warning had nowhere to go and was dropped -- the exact
// case where a lost result is most likely to go unnoticed, since the session is
// closing and will not be the place its owner looks again.
//
// This drives drainStopForClose, not drainStop. The drain-level pins call
// drainStop with a root they supply themselves, so they exercise the abandon
// branch and can never observe what the close path passed.
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
	found := false
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			found = true
		}
	}
	if !found {
		t.Fatal("the close drain abandoned an inline receipt without reporting the lost result to the root")
	}
}
