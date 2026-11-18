package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// A retried delivery acknowledgment that succeeded still wakes its receiver, and
// that wake can fail. The failure is a warning, and Reconcile publishes it from
// its deferred unlock -- before the drain ever reaches its bounded warning
// publisher. On the session's own delivery path that warning waits for the full
// close budget, so a stalled authoritative stream spends the time reserved for
// removing collectible worktrees.
//
// The wake is best-effort news about an acknowledgment that already committed, so
// it must be published on the bound the drain supplied: a responsive consumer
// still receives it, and an unresponsive one releases the drain.
func TestDelegateWakeWarningUsesTheDrainBound(t *testing.T) {
	// A stalled authoritative stream: the send can only be delivered to a
	// consumer that resumes, or released by a bound.
	newStalledRoot := func(t *testing.T) *Session {
		s := newTestSessionForEnvctx(t)
		s.events = make(chan events.SessionEvent, 1)
		s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
		s.authoritativeConsumer = true
		t.Cleanup(func() { s.authoritativeConsumer = false })
		return s
	}

	t.Run("a spent drain bound releases the wake warning", func(t *testing.T) {
		s := newStalledRoot(t)
		controller := s.delegateController
		if controller == nil {
			t.Fatal("session has no delegate controller: this test needs one to publish through")
		}

		// A bound the drain supplied, already spent.
		bound, cancelBound := context.WithCancel(t.Context())
		cancelBound()
		restore := controller.setPublicationBound(bound)
		t.Cleanup(restore)

		var parked atomic.Bool
		s.testOnlyBlockedSendEntered = func() { parked.Store(true) }
		t.Cleanup(func() { s.testOnlyBlockedSendEntered = nil })

		done := make(chan struct{})
		go func() {
			defer close(done)
			controller.reportDelegateWakeError(errTestWakeFailed)
		}()

		// It must return on the drain's bound rather than park on the stream.
		testwait.Until(t, "the wake warning to release on the drain's bound", func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		})
		if !parked.Load() {
			t.Fatal("the wake warning never reached the blocked-send boundary: the send was not the thing under test")
		}
	})
}

// errTestWakeFailed is the failure a wake reports in this test. It is a plain
// error rather than a message assertion target: the test checks the sink and the
// bound, never the wording.
var errTestWakeFailed = errors.New("wake refused")
