package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// The close retry must never take a parked diagnostic away from a publisher that
// is still running.
//
// A failed bounded send parks its copy and then releases the claim -- two steps
// that used to be separable. The retry lifted the copy FIRST and only then tried
// to take the claim, so this interleaving lost the diagnostic outright: the retry
// took the copy, lost the claim to the sender that had parked it, and returned
// with nothing pending; the sender then released the claim, and the session was
// announced with no diagnostic on the stream and no copy left to retry -- even
// for a consumer that resumed inside the close window.
//
// The state below is exactly that paused publisher: a copy parked with the claim
// still held. The retry must leave the copy where it is, and must deliver it once
// the publisher settles.
func TestCloseRetryDoesNotTakeAParkedDiagnosticFromAnActivePublisher(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.events = make(chan events.SessionEvent, 8)
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })
	if err := s.failClosed(errors.New("transcript refuses writes")); err == nil {
		t.Fatal("session did not fail closed: the retry needs a refusal to announce")
	}

	// The paused publisher: the copy is stored and the claim is STILL its own.
	held := events.ErrorData{Error: "parked by an in-flight bounded send"}
	s.failedClosed.pending.Store(&held)
	s.failedClosed.announced.Store(true)

	// A retry with almost no time: it meets the active publisher and must give
	// up without touching the copy.
	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	s.retryCloseFailClosed(spent)

	if s.failedClosed.pending.Load() == nil {
		t.Fatal("the retry removed the parked diagnostic while its publisher still held the claim: when " +
			"that publisher then releases the claim the session is announced with nothing sent and nothing " +
			"pending, which is the loss this retry exists to prevent")
	}

	// The publisher settles: it releases the claim and leaves the copy parked.
	if !s.failedClosed.announced.CompareAndSwap(true, false) {
		t.Fatal("fixture could not model the publisher's release")
	}
	retryCtx, cancelRetry := context.WithCancel(t.Context())
	t.Cleanup(cancelRetry)
	s.retryCloseFailClosed(retryCtx)

	if got := len(s.events); got != 1 {
		t.Fatalf("the stream holds %d diagnostic(s) after the publisher settled, want 1: a copy that survived "+
			"an active publisher must still be delivered once", got)
	}
	if s.failedClosed.pending.Load() != nil {
		t.Fatal("the delivered diagnostic is still parked: a delivered copy must not be retried again")
	}
}
