package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
)

// A bounded publication must be releasable by the CALLER's cancellation even
// after a close budget is published.
//
// The close budget replaced the caller's context outright. So a bounded send --
// a close drain's warning, a hook's lifecycle event, a watch invalidation --
// that parked on a full authoritative buffer after a close had published its
// budget could only be released by that budget, i.e. by the end of the whole
// cascade. The caller's own deadline, which is the thing that decided the send
// was worth making, had no effect. On a stalled consumer the publication then
// spent the budget the worktree removals needed, and otherwise collectible
// lanes were left on disk.
//
// The assertion is which deadline releases the send, not how long it took: a
// wall-clock ceiling under host load agrees with a wrong policy sometimes, and
// a mutex-backed observation of "the send returned" does not say why.
func TestBoundedPublicationIsReleasedByTheCallersCancellationUnderACloseBudget(t *testing.T) {
	s := &Session{id: "sess_bound", events: make(chan events.SessionEvent, 1)}
	s.authoritativeConsumer = true
	// The buffer is full, so the send below has nowhere to go and must park.
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})

	// A close budget is published, and it is FAR from expiring. If the caller's
	// cancellation is not armed on the parked select, only this budget can
	// release the send, and the test times out instead of passing.
	cascadeStart := time.Now()
	budget := publishCloseBudgetForTest(t, s, 30*time.Second)

	entered := make(chan struct{})
	var once bool
	s.testOnlyBlockedSendEntered = func() {
		if !once {
			once = true
			close(entered)
		}
	}

	// The caller's own bound: short, and the only thing this test cancels.
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()

	done := make(chan bool, 1)
	go func() {
		_, _, delivered := s.sendEventContext(callerCtx, events.WarningData{Message: "bounded"}, nil)
		done <- delivered
	}()

	select {
	case <-entered:
	case <-time.After(30 * time.Second): // TRIPWIRE: in-process, the send parks immediately
		t.Fatal("the bounded send never reached its parked select")
	}
	// Canceling the CALLER must release it, even though the close budget is
	// still live and has ~30 seconds on it.
	cancelCaller()

	select {
	case delivered := <-done:
		if delivered {
			t.Fatal("a send released before the buffer freed a slot reported delivery")
		}
	case <-time.After(30 * time.Second): // TRIPWIRE: the release is a channel close, not work
		t.Fatal("the caller's cancellation did not release the parked send: " +
			"the close budget had replaced it as the only bound, so the publication can only " +
			"be released by the end of the whole cascade")
	}
	if budget.Err() != nil {
		t.Fatal("the close budget expired during the test: it must remain live, so the release " +
			"above can only have come from the caller's cancellation")
	}
	if elapsed := time.Since(cascadeStart); elapsed >= 30*time.Second {
		t.Fatalf("the test ran %s, which is the cascade budget: the send was released by the "+
			"budget expiring, not by the caller's cancellation", elapsed)
	}
}

// publishCloseBudgetForTest publishes a close budget on s and returns it, so a
// send observes a live cascade exactly as it would during Close.
func publishCloseBudgetForTest(t *testing.T, s *Session, budget time.Duration) context.Context {
	t.Helper()
	// TRIPWIRE: the budget only has to outlive this in-process test, whose whole
	// point is that a much shorter caller deadline releases the send first.
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	t.Cleanup(cancel)
	s.closeCtxMu.Lock()
	s.closeCtx = ctx
	s.closeCtxMu.Unlock()
	t.Cleanup(func() {
		s.closeCtxMu.Lock()
		s.closeCtx = nil
		s.closeCtxMu.Unlock()
	})
	return ctx
}
