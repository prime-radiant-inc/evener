package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// The session's fail-closed diagnostic is at-most-once, and two publishers
// contend for it: the ordinary announce and the close's post-cleanup retry for a
// diagnostic a bounded send could not deliver. Whichever wins the claim is the
// one that publishes; the other must drop its copy silently rather than add a
// second.
//
// The winner is not deterministic and is not asserted. What must hold on every
// interleaving is the count: exactly one delivery per round.
func TestFailClosedAnnounceAndRetryContendForOneClaim(t *testing.T) {
	const rounds = 50
	for i := range rounds {
		s := newTestSessionForEnvctx(t)
		s.events = make(chan events.SessionEvent, 8)
		s.authoritativeConsumer = true
		if err := s.failClosed(errors.New("transcript refuses writes")); err == nil {
			t.Fatal("session did not fail closed: the race needs a refusal to announce")
		}

		// Park a diagnostic the way a failed bounded send does: unannounced, with
		// the payload held for the retry.
		held := events.ErrorData{Error: "parked"}
		s.failedClosed.pending.Store(&held)

		// Run in its own function so the cancel is released every round. A
		// defer in the loop body accumulated one live context per round, which
		// is the leak that makes a long contention test hang where a short one
		// passes.
		runFailClosedClaimRace(t, s)

		if got := len(s.events); got != 1 {
			t.Fatalf("round %d delivered %d diagnostics, want exactly 1: at-most-once announcement is broken", i, got)
		}
		if !s.failedClosed.announced.Load() {
			t.Fatalf("round %d left the claim open after delivering once", i)
		}
		// The channel is per-round, so the next round starts clean.
		s.authoritativeConsumer = false
	}
}

// runFailClosedClaimRace is one round of the announcement race: the ordinary
// announce and the close's post-cleanup retry contend for the single claim.
// It takes its own context so the cancel is deferred here, where it runs once,
// rather than in the caller's loop body.
func runFailClosedClaimRace(t *testing.T, s *Session) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var wg sync.WaitGroup
	wg.Go(func() { s.announceFailClosed() })
	wg.Go(func() { s.retryCloseFailClosed(ctx) })
	wg.Wait()

	if got := len(s.events); got != 1 {
		t.Fatalf("round delivered %d diagnostics, want exactly 1: at-most-once announcement is broken", got)
	}
	if !s.failedClosed.announced.Load() {
		t.Fatal("round left the claim open after delivering once")
	}
	// The channel is per-round, so the next round starts clean.
	s.authoritativeConsumer = false
}

// A retry whose own send fails must not keep the claim. Clearing pending and
// then dropping the send would lose the diagnostic with nothing left to retry --
// the same defect the claim-release fixed, one call later.

// Two close-triggered completions can both attempt the diagnostic while the
// stream stays full past their deadline. The second must not mark the session
// announced while a pending copy sits unclaimed, or the post-cleanup retry
// discards it and a consumer that resumes sees nothing.
func TestFailClosedRepeatedFailedAttemptsLeaveOneRetryableCopy(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })
	if err := s.failClosed(errors.New("transcript refuses writes")); err == nil {
		t.Fatal("session did not fail closed")
	}

	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()

	// Two expired attempts, the shape of two concurrent hook completions. Each
	// claims independently, as two callers racing the stream would.
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { s.announceFailClosedOn(bound) })
	}
	wg.Wait()

	if s.failedClosed.announced.Load() {
		t.Fatal("repeated failed attempts left the session reading as announced with nothing delivered")
	}
	if s.failedClosed.pending.Load() == nil {
		t.Fatal("repeated failed attempts left no copy for the post-cleanup retry")
	}

	// The retry can still deliver it once the consumer resumes.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-s.events
	}()
	testwait.Until(t, "the stream to free a slot for the diagnostic", func() bool {
		select {
		case <-drained:
			return true
		default:
			return false
		}
	})
	s.retryCloseFailClosed(t.Context())
	if !s.failedClosed.announced.Load() {
		t.Fatal("the retry after repeated failed attempts delivered nothing")
	}
	if len(s.events) != 1 {
		t.Fatalf("stream carries %d diagnostic(s), want exactly 1", len(s.events))
	}
}

func TestFailClosedRetryReleasesTheClaimWhenItsSendFails(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	// A stalled authoritative stream: the retry below cannot deliver.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })
	if err := s.failClosed(errors.New("transcript refuses writes")); err == nil {
		t.Fatal("session did not fail closed")
	}

	held := events.ErrorData{Error: "parked"}
	s.failedClosed.pending.Store(&held)

	// A bound the close supplied, already spent.
	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()
	s.retryCloseFailClosed(bound)

	if s.failedClosed.announced.Load() {
		t.Fatal("the failed retry kept the claim: the session reports itself announced with nothing delivered")
	}
	if s.failedClosed.pending.Load() == nil {
		t.Fatal("the failed retry dropped the diagnostic instead of holding it")
	}
}
