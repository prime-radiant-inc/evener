package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// A stop driver runs on a cancel-only context. When it is abandoned while a
// close budget is published, its pending send must be released by its own
// cancellation -- not wait out the cascade.
//
// Selecting the earlier deadline between the caller's context and the published
// close budget REPLACED the caller's arm: a cancel-only context has no deadline
// to compare, so the close budget won outright and the cancellation was
// discarded. The two are independent exit conditions.
func TestAbandonedDriverIsCancellationReleasesTheBlockedSend(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	// A stalled authoritative stream: nothing can land until something releases
	// the send.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	// A published close budget with a long window, so only the caller's own
	// cancellation can release the send in time for this test to observe it.
	closeCtx, cancelClose := context.WithCancel(context.Background())
	s.closeCtxMu.Lock()
	s.closeCtx = closeCtx
	s.closeCtxMu.Unlock()
	t.Cleanup(func() {
		s.closeCtxMu.Lock()
		s.closeCtx = nil
		s.closeCtxMu.Unlock()
		cancelClose()
	})

	var parked atomic.Bool
	s.testOnlyBlockedSendEntered = func() { parked.Store(true) }
	t.Cleanup(func() { s.testOnlyBlockedSendEntered = nil })

	driverCtx, cancelDriver := context.WithCancel(context.Background())
	t.Cleanup(cancelDriver)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.emitContext(driverCtx, events.WarningData{Message: "a driver's warning"})
	}()

	// Wait until the send has actually reached the blocked arm, so the
	// cancellation below is testing the park rather than racing its arrival.
	testwait.Until(t, "the driver's send to park on the stalled stream", parked.Load)

	// Abandoning the driver is its own exit condition.
	cancelDriver()

	testwait.Until(t, "the abandoned driver's send to release on its own cancellation", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

// The watch-activity flush that follows an event is SYNCHRONOUS with the send
// that caused it. A close drain's lost-result warning can take the last slot in a
// full authoritative stream; the flush then runs on the drain's goroutine, and on
// the ordinary path it waits out the whole close cascade -- spending the budget
// the worktree removals need, immediately after the warning itself was correctly
// bounded.
//
// The observation is the bound the invalidation's send runs on, which is the
// policy decision. A send's own outcome is a select among ready cases and would
// agree with a wrong policy part of the time.
func TestCloseDrainWatchFlushRunsOnTheDrainBound(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	if s.jobManager == nil {
		t.Fatal("session has no job manager: nothing to flush watch activity through")
	}

	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()

	var parked atomic.Bool
	s.testOnlyBlockedSendEntered = func() { parked.Store(true) }
	t.Cleanup(func() { s.testOnlyBlockedSendEntered = nil })

	// A full authoritative stream, so an unpublishable invalidation must be
	// released by the bound rather than delivered.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	// Something is pending for the flush to publish.
	s.jobManager.noteWatchActivity(s.ID())

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.jobManager.publishWatchActivityContext(bound)
	}()

	// The flush reaches the blocked-send boundary and returns on the bound.
	testwait.Until(t, "the bounded watch flush to return on the drain's bound", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	if !parked.Load() {
		t.Fatal("the watch invalidation never reached the blocked-send boundary: the send was not the thing under test")
	}
}
