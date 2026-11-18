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

// A bounded publication's job-manager observer must run on the same bound.
//
// emitWithProvenanceContext correctly bounded its own send, then handed the
// event to the UNBOUNDED observer. The watch-activity flush inside that observer
// is synchronous, so a publication that filled the last slot in a full stream
// left the flush waiting on the session's own delivery path -- the full close
// cascade. The stop deadline that released the send could not release the flush,
// so collectible lanes reached disposal with an expired budget.
//
// Both conditions are required to reach the wait, and this test supplies both:
// a pending invalidation for the flush to publish, and a full stream so the
// flush cannot land.
func TestBoundedPublicationFlushesWatchActivityOnItsBound(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	if s.jobManager == nil {
		t.Fatal("session has no job manager: nothing to flush watch activity through")
	}

	// A full authoritative stream.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	// A concurrent watch mutation has queued an invalidation for the flush.
	s.jobManager.noteWatchActivity(s.ID())

	var parked atomic.Bool
	s.testOnlyBlockedSendEntered = func() { parked.Store(true) }
	t.Cleanup(func() { s.testOnlyBlockedSendEntered = nil })

	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.emitWithProvenanceContext(bound, events.WarningData{Message: "a bounded publication"}, nil)
	}()

	// The publication reaches the blocked-send boundary and the whole call,
	// flush included, returns on the bound.
	testwait.Until(t, "the bounded publication and its watch flush to return on the bound", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	if !parked.Load() {
		t.Fatal("the publication never reached the blocked-send boundary: the send was not the thing under test")
	}
}

// A bounded watch flush that loses an invalidation must keep the receiver
// pending, because the flush clears the whole set before it sends.
//
// The flush takes the pending receivers, empties the set, and only then sends.
// If a bounded send expires -- a close warning took the last slot in a full
// authoritative stream -- the invalidation is gone from the set AND undelivered.
// When the consumer resumes, the next ordinary event finds nothing pending, so an
// open Watches view stays stale until the user refreshes by hand. The fix keeps
// an undelivered receiver in the set, merged with anything a concurrent watch
// change recorded while the flush was running.
func TestBoundedWatchFlushKeepsAnUndeliveredReceiverPending(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	if s.jobManager == nil {
		t.Fatal("session has no job manager: nothing to flush watch activity through")
	}

	// A full authoritative stream, so the invalidation genuinely cannot land.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	var parked atomic.Bool
	s.testOnlyBlockedSendEntered = func() { parked.Store(true) }
	t.Cleanup(func() { s.testOnlyBlockedSendEntered = nil })

	receiver := "delivery-the-consumer-cannot-accept"
	s.jobManager.noteWatchActivity(receiver)

	// An already-expired bound: the send reaches the blocked arm and gives up
	// on it, which is the condition a close drain meets when the cascade's
	// publication cutoff is spent.
	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()
	s.jobManager.publishWatchActivityContext(bound)

	if !parked.Load() {
		t.Fatal("the invalidation never reached the blocked-send boundary: the fixture did not stall the send, so it cannot observe a lost invalidation")
	}

	s.jobManager.watchActivityMu.Lock()
	pending := s.jobManager.watchActivityReceivers
	s.jobManager.watchActivityMu.Unlock()
	if _, held := pending[receiver]; !held {
		t.Fatalf("the receiver whose invalidation expired is no longer pending (%v): the flush cleared the set "+
			"before sending, so nothing will ever publish this invalidation and an open Watches view stays stale "+
			"until a manual refresh", pending)
	}
}
