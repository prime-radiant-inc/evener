package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
)

// The close drain waits for its Notification hook's completion callback to
// return. When that completion cannot be recorded -- the transcript write is
// refused, the case a shutting-down session is most likely to hit -- the failure
// is published as a diagnostic, and the drain is still waiting on it. Publishing
// that diagnostic on the close cascade's full deadline puts the disposal budget
// behind a send, after the stop's own half has already expired, which is how a
// collectible lane ends up left on disk.
//
// The discriminator is a full authoritative stream. A publication on a bound the
// drain supplied releases; one on the session's own path keeps waiting for the
// consumer, which is the deferral that must survive if the session is still open.
// Which of the two happened is decided before the call returns, so this does not
// race a select.
func TestHookCompletionDiagnosticKeepsTheCallersBound(t *testing.T) {
	completion := events.HookEndData{
		Event:    "Notification",
		HookType: "command",
		Matcher:  "*",
	}

	// A session whose transcript writes are refused, so the completion really does
	// raise a diagnostic: a silent success would satisfy the assertion below
	// without exercising the path at all. Its stream is full and its consumer
	// stalled, so a send ends only when a bound ends it.
	poisoned := func(t *testing.T) *Session {
		sess := newTestSessionForEnvctx(t)
		// A roomy stream for setup: the poisoning write fails and its pending
		// events are drained below, and neither may park.
		sess.events = make(chan events.SessionEvent, 64)
		attachEnvironmentPoisoningWrite(t, sess,
			errors.New("hook completion transcript write failure"),
			errors.New("hook completion transcript rollback failure"))
		if err := sess.maybeAppendEnvironmentContext(); err == nil {
			t.Fatal("setup: the poisoning append did not fail")
		}
		drainPendingEvents(sess)

		// Now stall it: the consumer is authoritative and has not resumed, so a
		// send ends only when a bound ends it.
		sess.authoritativeConsumer = true
		t.Cleanup(func() { sess.authoritativeConsumer = false })
		stalled := make(chan events.SessionEvent, 1)
		stalled <- events.New(events.HookStartData{Event: "occupies the only slot"})
		sess.eventsMu.Lock()
		sess.events = stalled
		sess.eventsMu.Unlock()
		return sess
	}

	t.Run("the drain's completion releases on its own bound", func(t *testing.T) {
		sess := poisoned(t)

		// The drain dispatches on a bound it supplied. Here that bound is already
		// spent, so the completion and every diagnostic it raises must give up
		// rather than park on a consumer that has not resumed.
		stopCtx, cancelStop := context.WithCancel(t.Context())
		cancelStop()

		returned := make(chan struct{})
		go func() {
			defer close(returned)
			sess.publishHookLifecycle(withPublicationBound(stopCtx), events.EventHookEnd, completion)
		}()

		select {
		case <-returned:
			// Released by the bound the drain supplied.
		case <-time.After(closeDrainHookStopBudget): // TRIPWIRE: an expired bound releases synchronously, so this only fires if it was discarded
			t.Fatal("the drain's hook completion did not release on the bound it supplied: it parked on the stream, so the drain would spend the teardown budget waiting for a consumer that never resumed")
		}
	})
}
