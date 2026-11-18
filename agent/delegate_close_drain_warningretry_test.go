package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// A lost-result warning names a receipt that is already abandoned and gone from
// the store, so no later reconciliation can settle it again. When the bounded
// first attempt expires against a full authoritative stream, dropping it is the
// loss this whole change exists to prevent -- and the specific case that a
// disclosure about the parked-send path does not cover: here the send does
// return, and the warning is lost anyway.
//
// The retry is the close's answer: hold the warning, and publish it once on what
// the cascade has left after the worktree removals have run.
func TestLostResultWarningSurvivesABoundedSendThatExpires(t *testing.T) {
	s := newTestSessionForEnvctx(t)

	// A full authoritative stream: the first attempt cannot land.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	// A typed fixture: the code and delegate ID are the fields a reader acts
	// on, and asserting them is checking the payload rather than comparing
	// rendered copy to a phrase the test also defines.
	warning := events.WarningData{
		Message:    "the inline delegate's result was lost",
		Code:       events.WarningCodeDelegateDeliveryResultLost,
		DelegateID: "dlg_lost_result",
	}
	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()

	s.emitCloseDrainWarnings(spent, []events.WarningData{warning})

	if held := s.closeDrainWarnings.Load(); held == nil || len(*held) != 1 {
		t.Fatalf("an expired warning was not retained for retry: held=%v", held)
	}

	// The removals are done and the consumer has resumed: the retry must deliver
	// the warning exactly once, on the close budget the teardown did not need.
	<-s.events
	s.retryCloseDrainWarnings(t.Context())

	if got := len(s.events); got != 1 {
		t.Fatalf("the retry delivered %d warnings, want exactly 1", got)
	}
	delivered := <-s.events
	got, ok := delivered.Data.(events.WarningData)
	if !ok {
		t.Fatalf("the retried event is not a warning: %T", delivered.Data)
	}
	if got.Code != warning.Code || got.DelegateID != warning.DelegateID {
		t.Fatalf("the retry delivered a different warning: code=%q delegate=%q, want code=%q delegate=%q",
			got.Code, got.DelegateID, warning.Code, warning.DelegateID)
	}
	if held := s.closeDrainWarnings.Load(); held != nil && len(*held) != 0 {
		t.Fatalf("the retry left %d warning(s) held after delivering", len(*held))
	}
}

// A first attempt that lands must not be held: holding it would deliver the same
// warning twice, which is the duplicate-report failure the fail-closed claim
// exists to prevent one resource over.
func TestLostResultWarningIsNotRetainedWhenItLandsFirst(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.events = make(chan events.SessionEvent, 8)
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	s.emitCloseDrainWarnings(t.Context(), []events.WarningData{{Message: "delivered on the first attempt"}})

	if held := s.closeDrainWarnings.Load(); held != nil && len(*held) != 0 {
		t.Fatalf("a delivered warning was retained for retry: it would be reported twice")
	}
	if got := len(s.events); got != 1 {
		t.Fatalf("the first attempt delivered %d warnings, want exactly 1", got)
	}
}
