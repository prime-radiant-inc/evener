package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

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
			withPublicationBound(spent), receiver, []string{"delegate:held"})
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
		withPublicationBound(spent), receiver, []string{"delegate:held"})

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
