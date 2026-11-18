package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/transcript"
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
	// The lifecycle callback is what puts HookStart and HookEnd on the session's
	// stream. Without it the runner dispatches and publishes nothing, so a test
	// built on the bare runner would pass even if the drain never launched a
	// hook at all. Wiring it through emitHookCompleted -- the production route --
	// makes every one of those sends a real send under the drain's cap.
	var lifecycleMu sync.Mutex
	var lifecycle []events.EventKind
	defer func() {
		lifecycleMu.Lock()
		defer lifecycleMu.Unlock()
		if len(lifecycle) < 2 {
			t.Errorf("runner published %d lifecycle events %v, want at least HOOK_START and HOOK_END: the hook did not run", len(lifecycle), lifecycle)
		}
	}()
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		lifecycleMu.Lock()
		lifecycle = append(lifecycle, kind)
		lifecycleMu.Unlock()
		if end, ok := data.(events.HookEndData); ok && kind == events.EventHookEnd {
			root.emitHookCompleted(end)
			return
		}
		root.emit(data)
	})
	root.hookRunner = runner

	// Count every send that actually parks, so the assertions below can require
	// the hook lifecycle to have reached the boundary rather than assuming it.
	var parked atomic.Int32
	root.testOnlyBlockedSendEntered = func() { parked.Add(1) }
	defer func() { root.testOnlyBlockedSendEntered = nil }()

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

	// The hook must actually have run and published: the warning fired it, and
	// its lifecycle events are the sends that park behind the warning. Without
	// this the test would pass on a drain that never launched a hook, which is
	// exactly the shape it exists to catch.
	if parked.Load() == 0 {
		t.Fatal("no send ever parked: the hook lifecycle never reached the blocked-send boundary, so this test proved nothing about bounding it")
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

// A warning and a repair can be settled by the same reconcile pass. The repair
// is an effect and may fail; the warning is news about a receipt the same pass
// abandoned, and no later pass can recreate it. If the repair's error is
// returned before the warning is published, the lost result becomes invisible on
// exactly the path where something else also went wrong.
//
// The attention cleanup here fails deterministically: its writer opener is set
// to refuse, so the plan is reached and rejected. The drain must still publish
// the lost-result warning, and must still return the cleanup's error.
func TestDelegateCloseDrainPublishesWarningBeforeFailingAttentionCleanup(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	c := f.controller
	f.root.events = make(chan events.SessionEvent, 8)

	// A wake the stop still owes becomes an attention cleanup plan in the same
	// pass that abandons the inline receipt.
	c.mu.Lock()
	if c.attention["dlg_target"] == nil {
		c.attention["dlg_target"] = &delegateAttentionState{}
	}
	c.attention["dlg_target"].wakeIDs = map[string]struct{}{"att_stranded": {}}
	c.attentionOpen = func(string, string) (*transcript.Writer, []transcript.Entry, error) {
		return nil, nil, errors.New("attention store unavailable")
	}
	c.mu.Unlock()

	err := c.drainStopForClose(tripwireCtx(t), c.stopForResult(f.stop))
	if err == nil {
		t.Fatal("close drain returned nil although the attention cleanup was refused")
	}
	reported := 0
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("lost-result warnings recorded on the closing root = %d, want exactly 1: the cleanup error must not carry the warning out unpublished", reported)
	}
}

// A Notification hook's completion is recorded in the transcript, and that write
// can fail. A transcript-write failure is an EventWarning, and an EventWarning
// dispatches the Notification hook -- so the ordinary warning path turns one
// failed write into another hook, whose completion fails the same way. Against a
// writer that keeps refusing, that is a chain of dispatches bounded only by the
// shutdown budget.
//
// The count is the whole assertion: the hook runs once, and its own failure
// never launches another. The failure still reaches the stream, as a diagnostic
// that does not dispatch.
func TestNotificationHookCompletionDoesNotDispatchAnotherHook(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	attachEnvironmentPoisoningWrite(t, sess,
		errors.New("hook completion transcript write failure"),
		errors.New("hook completion transcript rollback failure"))

	// The poisoned writer is established by the first failing durable write.
	if err := sess.maybeAppendEnvironmentContext(); err == nil {
		t.Fatal("setup: the poisoning append did not fail")
	}
	drainPendingEvents(sess)

	var dispatched atomic.Int32
	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf '%s\n' '{"systemMessage":"hook output"}'`,
		Timeout: 30,
	})
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		if kind == events.EventHookStart {
			dispatched.Add(1)
		}
	})
	sess.hookRunner = runner

	// Drive one hook completion through the production route. The transcript
	// write it performs is refused by the poisoned writer.
	drainPendingEvents(sess)
	sess.emitHookCompleted(events.HookEndData{
		Event:    string(plugin.HookNotification),
		HookType: "command",
		Matcher:  "*",
	})

	// The failure must be real, or a silent success would satisfy the count
	// below. Its diagnostic still reaches the stream.
	failures := 0
	for _, event := range drainPendingEvents(sess) {
		if event.Kind == events.EventWarning {
			failures++
		}
	}
	if failures == 0 {
		t.Fatal("no transcript-failure diagnostic was published: the hook completion's write did not actually fail, so this test proved nothing")
	}
	// That failure must not launch the hook that would fail the same way.
	if got := dispatched.Load(); got != 0 {
		t.Fatalf("Notification hook re-dispatches from a hook completion's own transcript failure = %d, want 0: the warning path must not be reachable from a hook's completion", got)
	}
}
