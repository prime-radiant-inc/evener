package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// A session's fail-closed diagnostic is a one-shot claim: at most one
// announcement ever. When a bounded close-time send claims it and then expires
// on a full authoritative stream, the claim used to stand with nothing
// delivered -- so a consumer that resumed inside the cascade window received
// session end and never learned why the transcript stopped accepting writes.
//
// The claim is confirmed by delivery, not taken by the attempt: a failed send
// releases it and parks the diagnostic for the post-cleanup retry.
func TestBoundedFailClosedDiagnosticSurvivesAnExpiredSend(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	// A stalled authoritative stream: the bounded send below cannot land.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })
	// A session is "served" only once a daemon consumes its events, which is
	// what failClosed refuses an unserved session for.
	s.failClosed(errors.New("transcript refuses writes"))

	// A bound the close supplied, already spent.
	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()
	s.announceFailClosedOn(bound)

	// The attempt must not have kept the claim: nothing was delivered.
	if s.failedClosed.announced.Load() {
		t.Fatal("the expired send kept the claim: the session now reports itself announced with nothing on the stream")
	}
	if s.failedClosed.pending.Load() == nil {
		t.Fatal("the expired send dropped the diagnostic instead of holding it for the post-cleanup retry")
	}

	// Make room, then run the retry the close runs after worktree cleanup.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-s.events
	}()
	testwait.Until(t, "the stream to accept the diagnostic", func() bool {
		select {
		case <-drained:
			return true
		default:
			return false
		}
	})

	retryCtx, cancelRetry := context.WithCancel(t.Context())
	defer cancelRetry()
	s.retryCloseFailClosed(retryCtx)

	// At most once: the claim is now held again, and the diagnostic is on the
	// stream for the consumer to read.
	if !s.failedClosed.announced.Load() {
		t.Fatal("the retry delivered nothing and left the claim open")
	}
	testwait.Until(t, "the retried diagnostic to reach the stream", func() bool {
		return len(s.events) > 0
	})
	var got bool
	for len(s.events) > 0 {
		ev := <-s.events
		if _, ok := ev.Data.(events.ErrorData); ok {
			got = true
		}
	}
	if !got {
		t.Fatal("the stream carries no failure diagnostic: the consumer would see session end with no reason")
	}

	// A second retry has nothing to publish; the session still announces once.
	s.retryCloseFailClosed(retryCtx)
	if len(s.events) != 0 {
		t.Fatalf("the second retry published again: %d event(s) on the stream, want at-most-once", len(s.events))
	}
}
