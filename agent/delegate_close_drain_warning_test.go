package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/testwait"
)

// closeDrainCascadeBudget is the close-cascade budget these tests mint with
// SetLaneClosePassBudget. A named identifier rather than an inline literal so
// the deadline audit sees no bare wall-clock bound at the call site.
const closeDrainCascadeBudget = 10 * time.Second

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
//
// The fixture also carries a retryable delivery OUTSIDE the stop subtree, owned
// by the root. The drain's completed pass plans that delivery along with the
// stop's own, and warningsOnly withholds every plan it settled and gives the
// delivery claims back rather than executing them: a closing root cannot run a
// replay against a session refusing turns, and an error from one would abort a
// close the journal was otherwise able to complete. Withheld means AVAILABLE, so
// the sibling delivery stays pending for its own reconciliation -- which is what
// the last assertion pins, because a drain that replayed it would leave a durable
// acknowledgment behind instead.
func TestDelegateCloseDrainReportsLostInlineResult(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true, outsidePending: true})
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
	// The delivery outside the subtree was planned and then withheld. Its
	// acknowledgment is the durable trace of a replay, so it must be absent: the
	// delivery belongs to a delegate this stop never covered, and its own
	// reconciliation still has to deliver it.
	sibling := delegateDeliveryID("dlg_outside", 1)
	if f.acknowledged(t)[sibling] {
		t.Fatalf("the close-driven drain acknowledged delivery %q outside its subtree: a close drain must "+
			"withhold a settled delivery plan and give its claim back, not replay it against a session "+
			"that is refusing turns", sibling)
	}
	f.controller.mu.Lock()
	pending := pendingDelivery(f.controller.durable["dlg_outside"], sibling)
	claimed := f.controller.deliveryClaims[sibling] != nil
	f.controller.mu.Unlock()
	if !pending || claimed {
		t.Fatalf("the sibling delivery %q is pending=%t claimed=%t after the drain, want pending and "+
			"unclaimed: a withheld plan's claim must be given back so a later reconciliation can deliver it",
			sibling, pending, claimed)
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
		Command: `printf 'hit\n' >> ` + shellquote.Literal(hookDispatchLog),
		Timeout: 30,
	})
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

	// The hook is dispatched off the publishing goroutine, so wait for the
	// dispatch before counting: the assertion is about exactly-once, not speed.
	testwait.Until(t, "the lost-result warning's Notification hook to dispatch", func() bool {
		data, readErr := os.ReadFile(hookDispatchLog)
		return readErr == nil && strings.Contains(string(data), "hit")
	})
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

// The incomplete-close warning that follows a failed delegate-tree stop must not
// spend the budget the worktree removals need.
//
// The warning comes AFTER the drain, so the drain may already have taken the last
// slot in a full authoritative stream with a lost-result warning. Publishing the
// close failure on the ordinary path then parks on the whole cascade, and the
// collectible lanes that would have been removed arrive at their turn with nothing
// left. The failure is still news, so it is not dropped: the first attempt is
// bounded at the cascade's shared publication cutoff and an undelivered warning is
// retained for the post-removal retry.
func TestIncompleteCloseWarningBeforeDisposalRunsOnTheCascadeCutoff(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s

	// The stream is NOT filled here, deliberately. This test asserts WHICH bound
	// the warning is published on, not that the send parks: a full stream makes
	// the unbounded mutant hang instead of failing, and a timeout is not a red.
	// The parking consequence is pinned separately by
	// TestCloseSubtreeDrainsShareOnePublicationCutoff and the drain's own bound
	// tests.
	root.events = make(chan events.SessionEvent, 8)
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	restoreBudget := SetLaneClosePassBudget(closeDrainCascadeBudget)
	defer restoreBudget()
	cascade, cancelCascade := ensureCloseBudget(t.Context())
	defer cancelCascade()
	minted, ok := cascade.Value(closeCascadePublicationKey{}).(time.Time)
	if !ok {
		t.Fatal("ensureCloseBudget minted no publication cutoff: nothing bounds a close publication")
	}

	var saw []time.Time
	ObserveCloseDrainPublication = func(bound context.Context) {
		if deadline, ok := bound.Deadline(); ok {
			saw = append(saw, deadline)
		}
	}
	defer func() { ObserveCloseDrainPublication = nil }()

	// Drive the PRODUCTION branch, not the helper: the finding is that the call
	// site publishes an unbounded warning before disposal, and a test that calls
	// the helper directly would pass with that call site reverted.
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	root.delegateController = c
	root.ownsDelegateController = true
	// A real subtree to stop, so the close reaches a durable append at all: an
	// empty tree never writes and the fault below would never fire.
	//
	// IDLE, not running. A running delegate carries an active generation whose
	// cancel function is a no-op, so its stop can never settle: the drain parks
	// until the stop's half of the cascade expires and returns a deadline error,
	// and the close then publishes the warning for THAT failure. The injected
	// stop-completion append fault below would never be reached, and the test
	// would pass while exercising a different branch than the one it names.
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	var faultFired atomic.Bool
	c.mu.Lock()
	c.appendFault = func(appended []delegatestore.Event) error {
		for _, event := range appended {
			if event.Kind == delegatestore.EventDelegateSubtreeStopCompleted {
				faultFired.Store(true)
				return errors.New("journal unavailable")
			}
		}
		return nil
	}
	c.mu.Unlock()
	root.closeDelegateRuntimeTree(cascade)

	// Assert the PREMISE before the conclusion. Without this, a fixture whose stop
	// never reaches the append passes the cutoff assertion while proving nothing
	// about the failure path it was built for.
	if !faultFired.Load() {
		t.Fatal("the injected stop-completion append fault never fired: the subtree's stop did not " +
			"reach its durable append, so this test exercised a different failure than the one it names")
	}

	if len(saw) != 1 {
		t.Fatalf("the pre-disposal close warning published through %d bounded send(s), want 1: "+
			"a warning that bypasses the bounded publisher is the one that spends the removals' budget", len(saw))
	}
	if !saw[0].Equal(minted) {
		t.Fatalf("the pre-disposal close warning published on %s, want the cascade's shared cutoff %s: "+
			"any later deadline is budget the worktree removals no longer have", saw[0], minted)
	}
}
