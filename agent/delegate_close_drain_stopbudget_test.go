package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
)

// A stop that gives up during a close must not cancel the cascade budget the rest
// of that close spends.
//
// closeTerminal's failed-stop branch used to call cancelBudget(), which took the
// budget that worktree disposal and retryCloseDrainWarnings spend. A warning
// retained before the stop -- an incomplete-close warning, or a lost-result
// warning the drain had already discovered -- was then discarded on the retry even
// though its receipt is already gone from the store and no later pass can rebuild
// it. The child joins are the only thing that must skip: a wedged child is a
// reason to stop waiting on that child, never a reason to stop reporting.
//
// The test drives the real closeTerminal so the branch is the production one, and
// asserts on the CONSEQUENCE rather than on the context: a warning retained before
// the stop is published by the post-disposal retry, which can only happen if the
// budget survived the failed stop.
func TestFailedDelegateStopKeepsTheCascadeBudgetForTheWarningRetry(t *testing.T) {
	// Short enough to keep the test fast, long enough that the retry below still
	// has time after the stop's own half is spent.
	shortenCloseCascadeBudget(t, 600*time.Millisecond)

	// A controller with a stop that can never complete: a committed, attached
	// generation keeps the stop's member set non-empty, so the bounded join parks
	// and gives up.
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	controller.rootRuntime = &Session{delegateController: controller}
	seedDelegateControllerIdle(t, controller, "dlg_target", "")
	commitAttachedDelegateControllerStart(t, controller, "dlg_target")
	_, cancelPlan, _, err := controller.StopSubtreeAndDrive(
		context.Background(), rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtreeAndDrive: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	s := newQueuePersistTestSession(t, t.TempDir())
	s.delegateController = controller
	s.ownsDelegateController = true

	// The warning the drain already discovered and could not publish in its own
	// window. This is what the retry exists to deliver.
	const retained = "a lost delegate result the close must not discard"
	s.retainUndeliveredCloseWarning(events.WarningData{Message: retained})

	var retryBudgets []bool
	ObserveCloseDrainRetryBudget = func(live bool) { retryBudgets = append(retryBudgets, live) }
	defer func() { ObserveCloseDrainRetryBudget = nil }()

	s.closeTerminal(context.Background(), closeOptions{})

	// The retry ran, and on a LIVE budget. This is the exact signal a whole-cascade
	// cancellation changes, and it cannot be satisfied by a send that happened to
	// find room in the buffer.
	if len(retryBudgets) == 0 {
		t.Fatal("the post-disposal warning retry never ran with a warning retained: this test says " +
			"nothing about the budget it would have received")
	}
	for i, live := range retryBudgets {
		if !live {
			t.Fatalf("retry %d of %d ran on a cancelled budget: a failed delegate stop cancelled the "+
				"whole close cascade, so the warning retry has nothing to spend and a warning whose "+
				"receipt is already gone from the store is discarded", i+1, len(retryBudgets))
		}
	}
	// The warning survived the failed stop and the disposal that followed.
	if held := s.closeDrainWarnings.Load(); held != nil && len(*held) != 0 {
		t.Fatalf("the close retained %d warning(s) after the post-disposal retry: the retry never ran "+
			"on a live budget, so a warning whose receipt is already gone from the store was discarded",
			len(*held))
	}
	// And it was published, not merely dequeued: the retry swaps the pending
	// warnings out before sending, so "not retained" alone would also be true of a
	// retry that dropped it.
	found := false
	for len(s.events) > 0 {
		ev := <-s.events
		warning, ok := warningData(ev.Data)
		if ok && warning.Message == retained {
			found = true
		}
	}
	if !found {
		t.Fatal("the retained lost-result warning never reached the stream: a failed delegate stop " +
			"cancelled the cascade budget the retry spends, so the warning the whole change exists to " +
			"preserve was dropped")
	}
}
