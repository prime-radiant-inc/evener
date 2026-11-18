package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
)

// stopTimeoutCascadeBudget is the close-cascade budget this file's cascade gets.
//
// It is a NAMED identifier rather than a literal at the call site because it is
// production behaviour under test, not a hang guard: the delegate stop's join
// takes half of it, and the remainder is the window the post-removal retries
// spend. The stop parks immediately and is released by its own deadline, so the
// split is exercised without the test's duration depending on host speed.
const stopTimeoutCascadeBudget = 1 * time.Second

// A retained close warning must outlive the cancellation a stop timeout performs.
//
// When a close's delegate stop times out, the close gives up the remaining joins
// on that wedged child so the same child is not joined again through its generic
// Session.Close path. The pre-disposal publication has by then already retained
// its incomplete-close warning for the post-removal retry, and a lost-result
// warning retained earlier rides the same slot. Those warnings name receipts that
// are already gone from the store, so the retry is their only chance to be
// reported.
//
// The defect: the same context served both jobs. Cancelling the cascade budget to
// give up a child join also cancelled the window the retry was going to spend, so
// a consumer that resumed before the original deadline still lost the warning.
//
// The test drives the REAL close. A fixture that reconstructed the two contexts
// and handed them to the retry itself would assert its own inputs and pass with
// the defect intact.
func TestRetainedWarningSurvivesAStopTimeout(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	restoreBudget := SetLaneClosePassBudget(stopTimeoutCascadeBudget)
	defer restoreBudget()

	// A delegate tree whose stop can never reconcile: an attached, active
	// generation keeps the stop's claim set non-empty, so the drain parks on
	// progress until its own half of the cascade expires and the tree close
	// reports a spent budget. That is the branch under test.
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerIdle(t, c, "dlg_wedged", "")
	commitAttachedDelegateControllerStart(t, c, "dlg_wedged")
	s.delegateController = c
	s.ownsDelegateController = true
	c.mu.Lock()
	c.rootRuntime = s
	c.mu.Unlock()

	// A full authoritative stream, so the first attempt cannot land and the
	// warning is retained. That retained copy is what the retry must still spend.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	warning := events.WarningData{
		Message:    "the inline delegate's result was lost",
		Code:       events.WarningCodeDelegateDeliveryResultLost,
		DelegateID: "dlg_lost_result",
	}
	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	s.emitCloseDrainWarnings(spent, []events.WarningData{warning})
	if held := s.closeDrainWarnings.Load(); held == nil || len(*held) != 1 {
		t.Fatalf("the expired first attempt retained %v warning(s), want exactly 1: "+
			"the retry this test exercises needs one to publish", held)
	}

	// Record what context the production close hands the retry. A spent context
	// and a live one differ only in an outcome that host load also moves, so the
	// context itself is the observation.
	var retryMu sync.Mutex
	var retryAtCallErr error
	var retryObserved bool
	s.testOnlyCloseWarningRetry = func(ctx context.Context) {
		// Evaluated AT CALL TIME. The close cancels its budget as it returns, so
		// a context kept and inspected afterwards would report a cancellation
		// this test is not asking about -- the question is whether the retry was
		// given a window when it ran.
		retryMu.Lock()
		retryAtCallErr = ctx.Err()
		retryObserved = true
		retryMu.Unlock()
	}
	t.Cleanup(func() { s.testOnlyCloseWarningRetry = nil })

	// A consumer that resumes partway through the close. It starts before the
	// close and drains continuously, so every send after the delegate stop finds
	// a free slot -- which is the "consumer resumed before the deadline" case the
	// retained warning has to survive.
	delivered := make(chan events.WarningData, 8)
	consumerStop := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case <-consumerStop:
				return
			case ev, ok := <-s.events:
				if !ok {
					return
				}
				if data, ok := ev.Data.(events.WarningData); ok {
					select {
					case delivered <- data:
					default:
					}
				}
			}
		}
	}()
	var consumerStopped sync.Once
	stopConsumer := func() {
		consumerStopped.Do(func() {
			close(consumerStop)
			<-consumerDone
		})
	}
	t.Cleanup(stopConsumer)

	// The close runs to completion: the stop times out, the removals run, and the
	// retries publish on what the cascade has left.
	s.close(context.Background(), closeOptions{forceTerminal: true})

	// Stop the consumer before closing the collection channel: the consumer is
	// the only writer, so this is what makes the close below safe.
	stopConsumer()

	retryMu.Lock()
	observed, atCallErr := retryObserved, retryAtCallErr
	retryMu.Unlock()
	if !observed {
		t.Fatal("the close never reached its post-removal warning retry: the retained warning was dropped silently")
	}
	// The retry's own context must not be the one the stop timeout spent. This is
	// the invariant in one sentence: giving up a child join is not giving up the
	// publication window.
	if atCallErr != nil {
		t.Fatalf("the post-removal retry ran on a context the stop timeout had already ended (%v): "+
			"a retained lost-result warning names a receipt that is gone from the store, so this "+
			"close is its last chance", atCallErr)
	}

	close(delivered)
	found := false
	for got := range delivered {
		if got.Code == warning.Code && got.DelegateID == warning.DelegateID {
			found = true
		}
	}
	if !found {
		t.Fatal("the retained lost-result warning never reached the resumed consumer: a stop timeout " +
			"that cancels the cascade budget discards the warning window the retry was holding")
	}
}

// A retained warning is published exactly once on the retry, on a context that
// still has a window.
//
// This is the outcome half, stated on its own so a duplicate publication cannot
// hide behind the close-path assertion above: the retry must deliver the warning
// once, and a spent retry context must not be what stops it.
func TestRetainedWarningIsPublishedOnceOnRetry(t *testing.T) {
	s := newTestSessionForEnvctx(t)

	// A full stream at the first attempt: the warning cannot land, so it is
	// retained. That retained copy is what the retry must be able to spend.
	s.events = make(chan events.SessionEvent, 1)
	s.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	s.authoritativeConsumer = true
	t.Cleanup(func() { s.authoritativeConsumer = false })

	warning := events.WarningData{
		Message:    "the inline delegate's result was lost",
		Code:       events.WarningCodeDelegateDeliveryResultLost,
		DelegateID: "dlg_lost_result",
	}
	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	s.emitCloseDrainWarnings(spent, []events.WarningData{warning})
	if held := s.closeDrainWarnings.Load(); held == nil || len(*held) != 1 {
		t.Fatalf("the expired first attempt retained %v warning(s), want exactly 1: "+
			"the retry this test exercises needs one to publish", held)
	}

	<-s.events
	budgetCtx, cancelBudget := context.WithCancel(t.Context())
	defer cancelBudget()
	s.retryCloseDrainWarnings(budgetCtx)

	if got := len(s.events); got != 1 {
		t.Fatalf("the post-removal retry delivered %d retained warning(s), want exactly 1", got)
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
	if left := s.closeDrainWarnings.Load(); left != nil && len(*left) != 0 {
		t.Fatalf("the retry left %d warning(s) held after delivering", len(*left))
	}
}
