package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// closeDrainBound is the publication context a close drain supplies: a marked
// bound, and the close-join mark that says its publications are on the path
// whose reconciliation the close waits for. Both marks are what production
// stamps in delegate_tree_stop.go, so a test that hand-builds one must build
// both or it is exercising a context no caller ever produces.
func closeDrainBound(ctx context.Context) context.Context {
	return withCloseJoinBound(withPublicationBound(ctx))
}

// The hold release that precedes a bounded arm takes the receiver's attention
// lock. On a close drain's retry it ran UNBOUNDED, so a receiver holding that
// lock across an environment event stalled in synchronous descendant forwarding
// parked the reconciliation past the publication cutoff -- and the budget it
// spent is the one the remaining collectible lanes need to be removed.
//
// The arm after it was already bounded. Bounding only the arm left this wait one
// call earlier eating the same budget, which is why the review names both: fixing
// one leaves the other.
//
// The discriminator is a lock held for the whole bound. An unbounded release
// returns only when the holder lets go, which this test does at the end; a bounded
// one gives up at its deadline and reports the skip.
func TestBoundedHoldReleaseGivesUpOnAHeldAttentionLock(t *testing.T) {
	s := newTestSessionForEnvctx(t)

	// A user-stop hold exists, so the release has work to do and cannot no-op.
	s.attentionMu.Lock()
	s.attentionAutomaticDriveHolds = map[string]struct{}{"delegate:held": {}}
	s.attentionMu.Unlock()

	// The receiver is inside an environment append that holds the lock across a
	// stalled publish. Nobody releases it until cleanup: the point is that the
	// bounded caller must not wait for it.
	s.attentionMu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(s.attentionMu.Unlock) }
	t.Cleanup(unlock)

	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()

	var ran atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		ran.Store(s.releaseAutomaticDriveHoldsBounded(spent, delegateHoldReleaseDelivery))
	}()

	// The observation is the call RETURNING, not a duration: an unbounded release
	// never returns while the lock is held, so waiting for this is the assertion.
	//
	// `done` is the SOLE completion signal, and it is closed by the goroutine's
	// own defer. Polling a flag the goroutine sets before returning would let a
	// preemption between the store and the deferred close fail a correct run,
	// which is the one thing this fixture must not do.
	awaitCloseFenceSignal(t, done, "the bounded hold release returning instead of waiting for the attention lock")

	if ran.Load() {
		t.Fatal("a release on a spent bound reported that it ran while another holder still had the lock")
	}

	// The holds stand: nothing was presented, so a later drive still finds the
	// stopped report held where the stop left it. This test HOLDS attentionMu --
	// that is the condition it creates -- so the map is read under that same
	// hold rather than by re-taking the lock.
	held := len(s.attentionAutomaticDriveHolds)
	if held != 1 {
		t.Fatalf("a release that gave up its bound cleared the holds (%d left): the row would be "+
			"presented alone, leaving the stopped report for a later generation", held)
	}
}

// The ordinary path is untouched: with no bound the release takes the lock
// outright, as it always has, and clears the holds.
func TestOrdinaryHoldReleaseStillTakesTheLock(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.attentionMu.Lock()
	s.attentionAutomaticDriveHolds = map[string]struct{}{"delegate:held": {}}
	s.attentionMu.Unlock()

	if !s.releaseAutomaticDriveHoldsBounded(noBound, delegateHoldReleaseArm) {
		t.Fatal("the ordinary release refused to run: it supplied no bound, so the lock must be taken outright")
	}
	s.attentionMu.Lock()
	held := len(s.attentionAutomaticDriveHolds)
	s.attentionMu.Unlock()
	if held != 0 {
		t.Fatalf("the ordinary release left %d hold(s): an unbounded release clears them all", held)
	}
}

// The whole wake must not park on that lock either. The wake path is where both
// waits the review names live: the hold release, then the bounded arm, then the
// root notification.
//
// The observable is the wake's RETURN, which an unbounded release never performs
// while the lock is held.
func TestBoundedWakeDoesNotParkOnAHeldAttentionLock(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	if root.delegateController == nil {
		t.Fatal("fixture session has no delegate controller")
	}

	// A resident NON-ROOT receiver: the root receiver takes a different path and
	// would not exercise the release under test.
	receiver := &Session{id: "child-receiver", delegateController: root.delegateController}
	receiver.attentionMu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(receiver.attentionMu.Unlock) }
	t.Cleanup(unlock)

	var notified atomic.Bool
	receiver.notifyFunc = func() { notified.Store(true) }

	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	t.Cleanup(cancelSpent)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = root.delegateController.wakeDeliveryReceiver(
			closeDrainBound(spent), receiver, []string{"delegate:held"})
	}()

	// `done` first and alone, for the reason given in the release test above.
	awaitCloseFenceSignal(t, done, "the bounded wake returning instead of waiting for the attention lock")

	// The receiver is NOT woken. Hold-before-notify is the ordering the ordinary
	// path depends on: the notify func drives the receiver synchronously, so a
	// wake fired while the holds still stand presents the delivered row alone and
	// leaves the stopped report for a later generation -- the immediate restart
	// the stop exists to prevent. A skipped release therefore skips the wake, and
	// the committed acknowledgment plus the durable row let a later drive present
	// both together.
	if notified.Load() {
		t.Fatal("a wake whose hold release gave up still notified the receiver: the drive would " +
			"present the delivered row with the stopped report still held back, which is the " +
			"immediate restart the hold exists to prevent")
	}
}

// The arm's own error fallback must not undo the bound the arm just honoured.
//
// The fallback wakes the receiver SYNCHRONOUSLY, and the production notify func
// reads pending attention and takes attentionMu with no bound of its own. So an
// arm that gave up at the caller's deadline used to spill straight into the
// unbounded wait it had just refused: the reconciliation the drain waits for
// parked past the publication cutoff anyway, and the budget it spent is the one
// the remaining collectible lanes need.
//
// The fixture makes both facts deterministic rather than timed. The release's
// own hook takes attentionMu as the release finishes, so the release SUCCEEDS
// and the arm that follows finds the lock taken; the arm then returns
// errDelegateAttentionArmSpent, and the wake must return without notifying.
//
// This cannot pass for the wrong reason: the fallback as written notifies on
// every error, so a green here means the spent condition specifically skipped
// it. The ordinary path's notify stays pinned by
// TestDelegateAttentionDeliveryArmsTheRootRetryForANonRootReceiver.
func TestBoundedArmSpentDoesNotFallBackToAnUnboundedNotify(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	if root.delegateController == nil {
		t.Fatal("fixture session has no delegate controller")
	}

	// A resident NON-ROOT receiver: the root receiver arms a different path.
	receiver := &Session{id: "child-receiver", delegateController: root.delegateController}
	// A user-stop hold gives the release work to do, so it fires its hook and
	// cannot no-op its way past the arm below.
	receiver.attentionAutomaticDriveHolds = map[string]struct{}{"delegate:held": {}}

	var notified atomic.Bool
	receiver.notifyFunc = func() { notified.Store(true) }

	// The hook runs after the release drops the lock, so the release runs and
	// the arm that follows finds attentionMu taken. Holding it here rather than
	// racing a second goroutine is what makes the split deterministic.
	var hookOnce sync.Once
	locked := false
	updateSessionTestConfig(receiver, func(cfg *testConfig) {
		cfg.delegateAttentionHoldReleased = func(site delegateHoldReleaseSite, released []string) {
			hookOnce.Do(func() {
				receiver.attentionMu.Lock()
				locked = true
			})
		}
	})
	t.Cleanup(func() {
		if locked {
			receiver.attentionMu.Unlock()
		}
	})

	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	t.Cleanup(cancelSpent)

	// Synchronous, so the hook's lock and the arm's refusal are ordered by the
	// call itself; an unbounded fallback here would hang rather than race.
	_ = root.delegateController.wakeDeliveryReceiver(
		closeDrainBound(spent), receiver, []string{"delegate:held"})

	if !locked {
		t.Fatal("the hold release never ran: the fixture never reached the arm whose fallback it pins")
	}
	if notified.Load() {
		t.Fatal("an arm that ran out of the caller's bound still woke the receiver: the synchronous " +
			"notify takes attentionMu without a bound, so the wake parks past the cutoff the drain " +
			"supplied -- the committed acknowledgment and the durable row are what carry the row to a " +
			"later drive")
	}
}

// A bound can be spent by REPORTING the arm's error, not only by the arm's own
// wait -- and the lock can change hands in that gap.
//
// The arm here refuses on a transcript read error while attentionMu is free, so
// it never reaches errDelegateAttentionArmSpent. Publishing that error consumes
// what is left of the cutoff, and a concurrent environment append then takes the
// lock. Deciding "the lock was free, so notify" at the earlier moment would wake
// the receiver into an unbounded wait on the lock it just lost, past the
// publication cutoff, into the budget the remaining collectible lanes need.
//
// So a caller that supplied a bound must not wake the receiver synchronously on
// ANY arm error. The wake that matters is preserved for the ordinary delivery
// path, which supplied no bound: TestDelegateAttentionDeliveryArmsTheRootRetryForANonRootReceiver
// and the ordinary counterpart below pin that side.
//
// This test holds no lock on purpose. The claim is about the gate, not about a
// timing window: with a live bound and an error that is NOT the spent sentinel,
// the old fallback woke the receiver and this one must not -- which is exactly
// the mutation that reverts the gate.
func TestBoundedNonSpentArmErrorDoesNotFallBackToAnUnboundedNotify(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	if root.delegateController == nil {
		t.Fatal("fixture session has no delegate controller")
	}

	receiver := &Session{id: "child-receiver", delegateController: root.delegateController}
	var notified atomic.Bool
	receiver.notifyFunc = func() { notified.Store(true) }
	t.Cleanup(func() {
		receiver.attentionMu.Lock()
		receiver.delegateAttentionArmIDs = nil
		receiver.resetDelegateAttentionArmRetryLocked()
		receiver.attentionMu.Unlock()
	})

	// A refusal that is NOT the spent sentinel: the arm gives up on its own read,
	// with the lock available.
	var armed atomic.Bool
	updateSessionTestConfig(receiver, func(cfg *testConfig) {
		cfg.delegateAttentionArmFault = func() error {
			armed.Store(true)
			return errors.New("test: arm refused on a transcript read")
		}
	})

	// A live, unspent bound: this is a bounded caller, and the point is that it
	// still must not wake, so the test cannot lean on the sentinel.
	bound, cancelBound := context.WithCancel(t.Context())
	t.Cleanup(cancelBound)

	_ = root.delegateController.wakeDeliveryReceiver(
		closeDrainBound(bound), receiver, []string{"delegate:held"})

	if !armed.Load() {
		t.Fatal("the arm fault never ran: the fixture never reached the arm whose fallback it pins")
	}
	if notified.Load() {
		t.Fatal("a bounded caller's arm refused on an error that is not the spent sentinel and the wake " +
			"still notified synchronously: reporting an error can spend the cutoff, and the notify then " +
			"waits on attentionMu without a bound -- the lock's availability at the decision is not its " +
			"availability at the wake")
	}
}

// The other side of the same gate: an ordinary delivery, which supplied no
// bound, keeps its immediate wake on any arm error. Without this, the fix above
// could be "never notify", which would strand the ordinary path.
func TestBoundedOrdinaryWakeStillNotifiesImmediately(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	if root.delegateController == nil {
		t.Fatal("fixture session has no delegate controller")
	}

	receiver := &Session{id: "child-receiver", delegateController: root.delegateController}
	var notified atomic.Bool
	receiver.notifyFunc = func() { notified.Store(true) }
	// The refusal below is retained for the arm's paced retry, and this stub has
	// no owning delegate ID, so every retry fails and re-arms. Retire the episode
	// under attentionMu on the way out, exactly as the neighboring test does, or
	// the timer outlives the test and fires against a torn-down fixture.
	t.Cleanup(func() {
		receiver.attentionMu.Lock()
		receiver.delegateAttentionArmIDs = nil
		receiver.resetDelegateAttentionArmRetryLocked()
		receiver.attentionMu.Unlock()
	})
	var armed atomic.Bool
	updateSessionTestConfig(receiver, func(cfg *testConfig) {
		cfg.delegateAttentionArmFault = func() error {
			armed.Store(true)
			return errors.New("test: arm refused")
		}
	})

	if err := root.delegateController.wakeDeliveryReceiver(noBound, receiver, []string{"delegate:held"}); err != nil {
		t.Fatalf("ordinary wake: %v", err)
	}
	if !armed.Load() {
		t.Fatal("the arm fault never ran: the fixture never reached the arm")
	}
	if !notified.Load() {
		t.Fatal("an ordinary delivery that supplied no bound did not wake the receiver on an arm refusal: " +
			"that wake is the delivery's own and must stay immediate")
	}
}

// The spent sentinel must skip the wake even for a caller that is NOT on a close
// join.
//
// This is the direction the close-join mark alone misses, and it is a different
// driver class: shutdown can abandon an ORDINARY stop driver after its hold
// release succeeded, while its arm is still waiting on an ungated receiver's
// attentionMu. That arm returns errDelegateAttentionArmSpent on an unmarked,
// merely-bounded context. Waking anyway re-enters the same mutex with no bound,
// keeps the abandoned driver alive, and a driver nobody joins holds up the
// delegate store's close.
//
// The ordering is the abandoned driver's own: the release succeeds, and only
// then does the bound expire with the lock in someone else's hands. The release
// hook is where that happens, so no clock and no race decides it -- the hook
// takes attentionMu and cancels the bound before the arm can run.
func TestAbandonedStopDriverSpentArmDoesNotBlockOnTheNotify(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	if root.delegateController == nil {
		t.Fatal("fixture session has no delegate controller")
	}

	receiver := &Session{id: "child-receiver", delegateController: root.delegateController}
	var notified atomic.Bool
	receiver.notifyFunc = func() { notified.Store(true) }
	// A user-stop hold gives the release work to do, so it runs and reaches its
	// hook instead of no-opping past the arm.
	receiver.attentionAutomaticDriveHolds = map[string]struct{}{"delegate:held": {}}

	// An ordinary stop driver's bound: a real one, with NO close-join mark.
	bound, cancelBound := context.WithCancel(t.Context())
	t.Cleanup(cancelBound)

	// The abandoned-driver moment: the release has succeeded, the lock is taken
	// by an in-flight environment append, and the driver's bound is gone.
	var hookOnce sync.Once
	locked := false
	updateSessionTestConfig(receiver, func(cfg *testConfig) {
		cfg.delegateAttentionHoldReleased = func(site delegateHoldReleaseSite, released []string) {
			hookOnce.Do(func() {
				receiver.attentionMu.Lock()
				locked = true
				cancelBound()
			})
		}
	})
	t.Cleanup(func() {
		if locked {
			receiver.attentionMu.Unlock()
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = root.delegateController.wakeDeliveryReceiver(
			withPublicationBound(bound), receiver, []string{"delegate:held"})
	}()

	// The wake must RETURN. An unbounded notify would block on the held lock
	// forever, so this wait is the assertion, not a duration.
	awaitCloseFenceSignal(t, done, "the abandoned stop driver's wake returning instead of waiting on the held lock")
	if !locked {
		t.Fatal("the release never ran: the fixture never reached the arm whose fallback it pins")
	}
	if notified.Load() {
		t.Fatal("the abandoned driver's arm gave up on its own bound and the wake still notified: the " +
			"notify waits on the same attentionMu with no bound, so the driver nobody joins never unwinds")
	}
}
