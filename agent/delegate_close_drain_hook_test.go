package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
)

// closeDrainHookStopBudget bounds the stop context these tests hand the drain.
// It is short because a publication that honours it returns promptly; the
// cascade behind it stays long, so a send that ignores the cap still fails
// loudly rather than passing by accident.
const closeDrainHookStopBudget = 300 * time.Millisecond

// A warning that fills the authoritative stream's last slot must not strand the
// Notification hook it fires. The hook's start event and output are published by
// the runner's own goroutines, which never see the drain's context, so a cap
// applied only to the warning would leave the hook waiting out the full close
// cascade after the stop's half had already expired.
//
// The observable is the drain returning on its own bound with the cascade still
// intact, plus the lane disposal that budget buys.
func TestDelegateCloseDrainBoundsNotificationHookPublication(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	root := r.s
	c := root.delegateController

	// The hook runs for real and returns a user message, which the notification
	// path publishes as a diagnostic warning. That publication is the send under
	// test: it happens inside the hook lifecycle, on the runner's goroutine.
	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf '%s\n' '{"systemMessage":"hook output"}'`,
		Timeout: 30,
	})
	root.hookRunner = runner

	stop := seedLostInlineStopForClose(t, c)

	// One slot, empty, with nothing reading: the warning takes it, and every
	// send behind the warning -- the hook's start event, its output -- parks.
	root.events = make(chan events.SessionEvent, 1)
	root.authoritativeConsumer = true
	defer func() {
		root.authoritativeConsumer = false
	}()

	restoreBudget := SetLaneClosePassBudget(closeDrainCascadeBudget)
	defer restoreBudget()
	cascade, cancelCascade := ensureCloseBudget(context.Background())
	defer cancelCascade()
	stopCtx, cancelStop := context.WithTimeout(cascade, closeDrainHookStopBudget)
	defer cancelStop()

	root.closeCtxMu.Lock()
	root.closeCtx = cascade
	root.closeCtxMu.Unlock()
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()

	if err := c.drainStopForClose(stopCtx, stop); err != nil {
		t.Fatalf("close drain = %v, want the hook lifecycle droppable on the stop bound", err)
	}
	if !delegateStopDone(stop) {
		t.Fatal("the close drain did not complete the stop it owned")
	}

	// The stalled stream is gone; disposal runs as the close runs it, on
	// whatever the drain left of the cascade.
	root.authoritativeConsumer = false
	root.disposeDelegateLanesAtClose(cascade)
	if lanePresent(lanePath) {
		t.Fatalf("isolation lane %s was left on disk: the hook lifecycle outlived the stop bound", lanePath)
	}
	if !r.stableDisposalClosurePresent(t, delegateID) {
		t.Fatalf("isolation lane %s was removed without a durable disposed mark", delegateID)
	}
	if err := cascade.Err(); err != nil {
		t.Fatalf("close cascade budget spent by the drain: %v", err)
	}
}

// A lost result is discovered by removing the receipt that can no longer be
// answered, so its warning exists only in the pass that discovered it. When the
// stop-completion journal append in that same pass fails, the warning must still
// reach the closing root: an error path is not an exemption from reporting.
//
// The journal append is forced to fail, so this test cannot pass by the append
// succeeding. It asserts the warning is published and the original error is still
// returned.
func TestDelegateCloseDrainPublishesWarningWhenStopCompletionAppendFails(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	c := f.controller
	f.root.events = make(chan events.SessionEvent, 8)

	// Fail only the stop-completion append: every other durable write in the
	// drain succeeds, so the abandoned receipt and its warning are reached
	// exactly as they are in production.
	c.mu.Lock()
	c.appendFault = func(appended []delegatestore.Event) error {
		for _, event := range appended {
			if event.Kind == delegatestore.EventDelegateSubtreeStopCompleted {
				return errors.New("journal unavailable")
			}
		}
		return nil
	}
	c.mu.Unlock()

	err := c.drainStopForClose(tripwireCtx(t), c.stopForResult(f.stop))
	if err == nil {
		t.Fatal("close drain returned nil although the stop-completion append failed")
	}
	// The warning is the fact that cannot be reconstructed: the receipt is gone.
	reported := 0
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("lost-result warnings recorded on the closing root = %d, want exactly 1: the receipt was abandoned and cannot be reported by any later pass", reported)
	}
	stored, readErr := delegatestore.ReadEvents(f.storePath)
	if readErr != nil {
		t.Fatalf("read delegate events: %v", readErr)
	}
	if delegateEventsStopCompleted(stored, "dlg_target") {
		t.Fatal("the fixture's stop-completion append was not actually refused; this test proved nothing")
	}
}
