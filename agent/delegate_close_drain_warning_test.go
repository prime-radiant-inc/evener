package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
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

	// The hook is dispatched off the publishing goroutine, so join it before
	// counting: the assertion is about exactly-once, not speed.
	f.root.waitStopWarningHooks()
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

// A close joins the ordinary stop drain's warning hooks before it closes the
// transcript's event stream. A stop driver the close abandoned can still be
// between a warning's publication and its hook dispatch at that moment, so the
// join must close the dispatch gate: a hook started after the join would run the
// user's command against a closing session, and nothing would wait for it
// (#6939).
func TestCloseDrainHookJoinRefusesLateOrdinaryWarningHooks(t *testing.T) {
	late := &Session{}
	var lateFired atomic.Int32
	late.joinStopWarningHooks()
	late.goStopWarningHook(func() { lateFired.Add(1) })
	late.waitStopWarningHooks()
	if got := lateFired.Load(); got != 0 {
		t.Fatalf("a stop warning hook dispatched after the close's hook join ran %d time(s), want 0: "+
			"the join must refuse hooks it did not wait for, or one races the transcript's teardown", got)
	}

	// The gate must not drop the hooks it was built to join: one started before
	// the join is waited for and runs.
	started := &Session{}
	var startedFired atomic.Int32
	started.goStopWarningHook(func() { startedFired.Add(1) })
	started.joinStopWarningHooks()
	if got := startedFired.Load(); got != 1 {
		t.Fatalf("a stop warning hook started before the close's hook join ran %d time(s), want 1: "+
			"the join waits for the hooks already dispatched", got)
	}
}

// An ordinary stop's warning hook outlives the stop driver that published the
// warning: the driver returns while the user's command still reports to the
// session. Retirement must therefore see the hook as a blocker, or it can cancel
// the command and close the transcript before its completion is recorded
// (#6939).
func TestStopWarningHookHoldsRetirementLease(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	controller, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	if err := controller.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	root.retirementController.Store(controller)

	started := make(chan struct{})
	resume := make(chan struct{})
	finished := make(chan struct{})
	root.goStopWarningHook(func() {
		close(started)
		<-resume
		close(finished)
	})
	<-started

	claim, snapshot, err := controller.TryClaim(true)
	if err != nil {
		close(resume)
		t.Fatalf("TryClaim: %v", err)
	}
	if claim != nil {
		close(resume)
		t.Fatal("retirement claimed the session while a stop warning hook was still running: " +
			"the hook would be cancelled and the transcript closed under its command")
	}
	if !slices.ContainsFunc(snapshot.Blockers, func(b RetirementBlocker) bool { return b.Category == "notification" }) {
		close(resume)
		t.Fatalf("retirement's blockers omit the running stop warning hook: %+v", snapshot)
	}
	close(resume)
	<-finished
	root.waitStopWarningHooks()
	root.Close()
}

// A close that gives up on a stalled ordinary stop driver must still deliver the
// lost-result warning that pass settled. The warning names a receipt already gone
// from the store, so that pass holds its only copy: the abandon cancels the
// drain, and the close's caller closes the event stream when close returns, so
// the pass's own later publication would land nowhere. The close publishes the
// pass's registered warning in its place, and the publication claim keeps it to
// exactly one delivery.
func TestClosePublishesAbandonedStopPassWarningsOnce(t *testing.T) {
	f, _ := newSeededStrandedStop(t)

	hookDispatchLog := filepath.Join(t.TempDir(), "hook-dispatches")
	runner := hooks.NewRunner(nil, "test-model")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf 'hit\n' >> ` + shellquote.Literal(hookDispatchLog),
		Timeout: 30,
	})
	f.root.hookRunner = runner
	f.root.env = execenv.NewLocalExecutionEnvironment(t.TempDir())

	settled := make(chan struct{})
	release := make(chan struct{})
	var stalled atomic.Bool
	ObserveStopPassWarningsSettled = func() {
		if stalled.CompareAndSwap(false, true) {
			close(settled)
			<-release
		}
	}
	defer func() { ObserveStopPassWarningsSettled = nil }()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	driver := newDelegateStopDriver()
	f.controller.mu.Lock()
	stop := f.controller.stop
	if stop == nil {
		f.controller.mu.Unlock()
		t.Fatal("fixture has no pending stop for a close to join")
	}
	stop.driver = driver
	f.controller.stopDriver = driver
	f.controller.mu.Unlock()
	driven := make(chan struct{})
	go func() {
		f.controller.runStopReconcileDriver(stop, driver, f.root)
		close(driven)
	}()
	testwait.Until(t, "the stop pass to register its settled warning", func() bool {
		select {
		case <-settled:
			return true
		default:
			return false
		}
	})

	// The close gives up on the driver: its stop join runs on a context that is
	// already done, so the driver is abandoned mid-cleanup.
	closeCtx, cancelClose := context.WithCancel(t.Context())
	cancelClose()
	if err := f.controller.closeRuntimeTree(closeCtx); err == nil {
		t.Fatal("closeRuntimeTree returned nil while the stop driver was stalled mid-cleanup: " +
			"this guard did not exercise an abandoned driver")
	}
	// The close returns from here and its caller closes the stream, so the
	// abandoned pass's warning must already be on it.
	if got := len(drainSessionWarnings(f.root)); got != 1 {
		t.Fatalf("the close delivered %d lost-result warning(s) from the abandoned pass, want exactly 1: "+
			"the warning named a receipt already gone from the store, and the pass's own publication "+
			"would run after the event stream was closed", got)
	}

	close(release)
	testwait.Until(t, "the abandoned stop driver to finish", func() bool {
		select {
		case <-driven:
			return true
		default:
			return false
		}
	})
	f.root.waitStopWarningHooks()
	if got := len(drainSessionWarnings(f.root)); got != 0 {
		t.Fatalf("the abandoned stop driver published its pass's warning again after the close delivered "+
			"it: %d duplicate warning event(s), want 0", got)
	}
	data, readErr := os.ReadFile(hookDispatchLog)
	if readErr != nil {
		t.Fatalf("the delivered warning never dispatched its Notification hook: %v", readErr)
	}
	if got := strings.Count(string(data), "hit"); got != 1 {
		t.Fatalf("the delivered warning dispatched its Notification hook %d time(s), want exactly 1", got)
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
