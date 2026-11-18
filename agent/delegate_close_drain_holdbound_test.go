package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/internal/testwait"
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

	var returned, ran atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		ran.Store(s.releaseAutomaticDriveHoldsBounded(spent, delegateHoldReleaseDelivery))
		returned.Store(true)
	}()

	// The observation is the call RETURNING, not a duration: an unbounded release
	// never returns while the lock is held, so waiting for this is the assertion.
	testwait.Until(t, "the bounded hold release to return instead of waiting for the attention lock", returned.Load)

	if ran.Load() {
		t.Fatal("a release on a spent bound reported that it ran while another holder still had the lock")
	}
	select {
	case <-done:
	default:
		t.Fatal("the release reported its result before returning")
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

	var returned atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = root.delegateController.wakeDeliveryReceiver(
			withPublicationBound(spent), receiver, []string{"delegate:held"})
		returned.Store(true)
	}()

	testwait.Until(t, "the bounded wake to return instead of waiting for the attention lock", returned.Load)
	<-done

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
