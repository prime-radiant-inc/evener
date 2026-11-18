package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
)

// A session close that abandons an inline delivery -- its owning tool commit is
// gone and no owner can still answer it -- must record the lost result. That
// notice is the one a closing session most needs, because the result is gone and
// the session is about to stop being where its owner would look (#6308).
//
// The close-driven drain (drainStopForClose) runs the same reconciliation as the
// stop driver and used to hand it a nil root, which dropped every plan the pass
// settled, warnings included. It now passes the controller's root, the receiver
// the stop driver already uses. The root outlives the drain: bootstrap attaches
// it once and never clears it, and Session.closeTerminal closes the root's event
// stream only after closeOwnedDelegateRuntimeTree has returned.
//
// The test drives drainStopForClose itself rather than closeRuntimeTree so it
// observes exactly the root this drain publishes to; the surrounding close path
// is what calls it (closeRuntimeTree, and joinOrDrainStopForClose's fallback).
func TestDelegateCloseDrainReportsLostInlineResult(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	f.root.events = make(chan events.SessionEvent, 4)
	if err := f.controller.drainStopForClose(tripwireCtx(t), f.controller.stopForResult(f.stop)); err != nil {
		t.Fatalf("close-driven drain with a lost inline commit = %v, want the abandoned receipt to settle the stop", err)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("the close-driven drain did not complete the stop after abandoning the inline receipt")
	}
	f.controller.mu.Lock()
	receipts := len(f.controller.deliveries)
	f.controller.mu.Unlock()
	if receipts != 0 {
		t.Fatalf("delivery admissions after the drain abandoned the inline receipt = %d, want 0", receipts)
	}
	stored, err := delegatestore.ReadEvents(f.storePath)
	if err != nil {
		t.Fatalf("read delegate events: %v", err)
	}
	// The result was lost, not delivered: the drain must not leave a durable
	// acknowledgment behind for the abandoned delivery.
	if delegateEventsAcknowledgeDelivery(stored, f.deliveries[0].deliveryID) {
		t.Fatalf("the drain acknowledged the abandoned inline delivery %q", f.deliveries[0].deliveryID)
	}
	reported := 0
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("lost-result warnings recorded on the closing root = %d, want exactly 1", reported)
	}
}

// The ordinary stop driver must keep the guarantee the ordinary path had: a
// configured Notification hook fires for a lost-result warning even when the
// best-effort event buffer is full.
//
// Routing the ordinary stop's warnings through emitCloseDrainWarnings made the
// hook a CONSEQUENCE of channel delivery. That helper retains an undelivered
// warning and fires the hook only when the send lands, and the close-time retry
// deliberately never redispatches -- so a full buffer silently removed a
// notification that used to arrive.
//
// The count assertion is the other half of the fix: the obvious repair
// dispatches on retain and again on retry, and a duplicated operator
// notification is its own defect. Exactly one dispatch is the contract, and the
// ordinary path retains nothing, so the retry has nothing to duplicate.
func TestOrdinaryStopDrainDispatchesTheLostResultHookOnce(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	if f.root == nil {
		t.Fatal("fixture produced no root: the drain publishes through it")
	}

	// A Notification hook that appends one line per dispatch to a file, which is
	// the only observation that cannot agree with a wrong policy by accident.
	hookDispatchLog := filepath.Join(t.TempDir(), "hook-dispatches")
	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf 'hit\n' >> ` + hookDispatchLog,
		Timeout: 30,
	})
	runner.SetEventCallbackContext(func(_ context.Context, _ events.EventKind, _ events.EventData) {})
	f.root.hookRunner = runner
	// The fixture's root is a bare Session; a dispatched hook needs the
	// environment it reports to its command.
	f.root.env = execenv.NewLocalExecutionEnvironment(t.TempDir())

	// The best-effort buffer is FULL, which is the condition that used to drop
	// the notification: the event cannot land, and the hook must fire anyway.
	f.root.events = make(chan events.SessionEvent, 1)
	f.root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})

	// The ordinary stop driver, not the close drain.
	if err := f.controller.drainStopAbandonable(tripwireCtx(t), f.controller.stopForResult(f.stop), f.root, nil, false); err != nil {
		t.Fatalf("ordinary stop drain with a lost inline commit = %v", err)
	}

	// The close-time retry must not be the thing that delivers it.
	f.root.retryCloseDrainWarnings(tripwireCtx(t))

	data, readErr := os.ReadFile(hookDispatchLog)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read hook dispatch log: %v", readErr)
	}
	got := strings.Count(string(data), "hit")
	if got != 1 {
		t.Fatalf("the lost-result warning dispatched its Notification hook %d time(s), want exactly 1: "+
			"a full buffer must not drop the notification, and a retry must not duplicate it", got)
	}
}
