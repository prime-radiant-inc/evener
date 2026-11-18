package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// A close warning that reached the parent's stream by the synchronous descendant
// forward is PUBLISHED, even though this session's own unread channel refused it.
//
// A resident delegate has an unread local channel by design: a child reaches its
// parent through callbacks, and waiting on the channel would wedge it. So the
// local send reports a drop while the forward -- the route that actually carries
// the news -- succeeds. Counting only the local send as delivery made the close
// publisher treat a warning that had already arrived as failed: it skipped the
// Notification hook, retained the warning, and the close-time retry forwarded a
// SECOND copy to AppWire. The owner was told twice about one lost result.
//
// Both directions are asserted here, because a fix that simply treated every
// failed local send as success would stop the retry in the case where nothing
// reached anyone.
func TestCloseWarningCountsDescendantForwardingAsPublication(t *testing.T) {
	newReceiver := func(t *testing.T, forwarded *atomic.Int32) *Session {
		s := newTestSessionForEnvctx(t)
		// One slot, already taken: the local send can never land, which is the
		// resident delegate's permanent condition.
		s.events = make(chan events.SessionEvent, 1)
		s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
		s.authoritativeConsumer = true
		t.Cleanup(func() { s.authoritativeConsumer = false })
		s.mu.Lock()
		s.descendantEvent = func(events.SessionEvent) { forwarded.Add(1) }
		s.mu.Unlock()
		return s
	}

	t.Run("a forwarded warning is published and not retained", func(t *testing.T) {
		var forwarded atomic.Int32
		s := newReceiver(t, &forwarded)

		warning := events.WarningData{
			Message:    "the inline delegate's result was lost",
			Code:       events.WarningCodeDelegateDeliveryResultLost,
			DelegateID: "dlg_lost_result",
		}
		spent, cancelSpent := context.WithCancel(t.Context())
		cancelSpent()
		s.emitCloseDrainWarnings(spent, []events.WarningData{warning})

		if got := forwarded.Load(); got != 1 {
			t.Fatalf("the warning reached the parent %d time(s), want exactly 1: this case needs the "+
				"forward route to have carried it, or it tests nothing", got)
		}
		if held := s.closeDrainWarnings.Load(); held != nil && len(*held) != 0 {
			t.Fatalf("a warning the forward already delivered was retained for retry (%d held): the "+
				"retry forwards a second copy to the parent, so the owner is told twice", len(*held))
		}
	})

	t.Run("a warning that reached neither route is still retained", func(t *testing.T) {
		var forwarded atomic.Int32
		s := newReceiver(t, &forwarded)
		// No parent to forward to: the local channel is the only route, and it is
		// full. Nothing reached anyone, so the retry is the warning's last chance.
		s.mu.Lock()
		s.descendantEvent = nil
		s.mu.Unlock()

		warning := events.WarningData{Code: events.WarningCodeDelegateDeliveryResultLost, DelegateID: "dlg_lost_result"}
		spent, cancelSpent := context.WithCancel(t.Context())
		cancelSpent()
		s.emitCloseDrainWarnings(spent, []events.WarningData{warning})

		if got := forwarded.Load(); got != 0 {
			t.Fatalf("a session with no parent forwarder forwarded %d event(s)", got)
		}
		held := s.closeDrainWarnings.Load()
		if held == nil || len(*held) != 1 {
			t.Fatalf("a warning that reached neither the local stream nor a parent was held %v times, "+
				"want exactly 1: dropping it loses the lost result entirely", held)
		}
	})
}
