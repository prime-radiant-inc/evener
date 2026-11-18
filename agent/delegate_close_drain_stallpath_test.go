package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/internal/testwait"
)

// A close that runs out of time waiting for an attention start's reservation
// must not then wait for that same reservation with no deadline at all.
//
// The start is admitted by a journal append that then fails, so it keeps its
// reservation until its own retry settles. The close gives up on the reservation
// -- that is the stop's join budget -- and reaches worktree disposal with the
// budget the retained-warning retry was supposed to run on. Disposal's
// resumability closure then took the SAME reservation with a bare receive: the
// lane's durable closure could not be written, nothing after it could be, and
// the close hung before the retry it had money left for.
//
// Bounded, the closure reports the lane busy: nothing is written, the lane stays
// intact on disk rather than half-removed, and the close continues.
func TestCloseDisposalDoesNotWaitOutAnAttentionReservation(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, _, _ := r.seedIsolationLane(t)
	root := r.s
	c := root.delegateController

	// The admitted-but-unsettled start. Its done channel is what the closure
	// waits on; nothing closes it while the close runs, which is exactly the
	// state a failing append's retry leaves for the duration of a close.
	reservation := make(chan struct{})
	c.mu.Lock()
	if c.reservations == nil {
		c.reservations = make(map[uint64]*delegateStartRecord)
	}
	token := uint64(1)
	for existing := range c.reservations {
		if existing >= token {
			token = existing + 1
		}
	}
	c.reservations[token] = &delegateStartRecord{
		token:             token,
		delegateID:        delegateID,
		trigger:           delegatestore.TriggerAttention,
		attentionAdmitted: true,
		done:              reservation,
	}
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		delete(c.reservations, token)
		c.mu.Unlock()
		close(reservation)
	})

	// A bound the close supplied, already spent: the join ran out of time before
	// disposal reached this lane.
	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()
	t.Cleanup(cancelBound)

	var (
		done     = make(chan struct{})
		closeErr error
		busy     bool
	)
	go func() {
		defer close(done)
		_, busy, _, closeErr = c.closeStableWorktreeResumability(bound, root, delegateID, stableWorktreeDisposalReason, true)
	}()
	awaitCloseFenceSignal(t, done, "the resumability closure releasing on its spent bound")

	if closeErr != nil {
		t.Fatalf("closure returned %v, want the busy refusal: a spent bound reports the lane busy, it does not "+
			"fail the close with an error the caller has to interpret", closeErr)
	}
	if !busy {
		t.Fatal("the closure reported the lane disposed while its admission reservation was still active: " +
			"nothing was written, so it must report the lane busy and leave it intact")
	}
	// The durable record is untouched, which is what makes the refusal honest.
	if r.stableDisposalClosurePresent(t, delegateID) {
		t.Fatal("a resumability closure was recorded for a lane whose attention start was still reserved: " +
			"the durable record would claim a closure the close never made")
	}
}

// A close-driven arm must not wrap the receiver's wake around the join.
//
// The arm acquires every lock within its bound, so reaching the wake proves it
// was in time. The wake then runs the production callback, which reads pending
// attention and takes attentionMu AGAIN with no bound: an environment append
// that takes that mutex after the final hold release stalls in synchronous
// descendant forwarding, and the close waits out the budget its worktree
// removals need on a wake that had nothing left to prove.
//
// The stub callback below takes the same mutex the production one does, and the
// test holds it, so the cost is reproduced rather than assumed: deferred, the
// call returns with the lock still held; synchronous, it parks.
func TestCloseDrivenArmDoesNotWrapAWakeAroundTheHeldAttentionLock(t *testing.T) {
	receiver := &Session{id: "child-receiver"}
	var notified atomic.Bool
	// The production notify func's load: it acquires attentionMu to read pending
	// attention before it drives anything.
	receiver.notifyFunc = func() {
		receiver.attentionMu.Lock()
		defer receiver.attentionMu.Unlock()
		notified.Store(true)
	}

	receiver.attentionMu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			receiver.attentionMu.Unlock()
		}
	})

	bound, cancelBound := context.WithCancel(t.Context())
	t.Cleanup(cancelBound)

	done := make(chan struct{})
	go func() {
		defer close(done)
		receiver.wakeAfterDelegateArm(closeDrainBound(bound))
	}()
	// A synchronous wake parks on the held mutex and never closes done.
	awaitCloseFenceSignal(t, done, "the close-driven wake returning while the attention lock is held")

	if notified.Load() {
		t.Fatal("the wake reported the receiver woken while the attention lock it needs was held: it must " +
			"run the callback, not merely fire it")
	}
	// Deferred, not dropped: releasing the lock must let the wake land.
	receiver.attentionMu.Unlock()
	locked = false
	testwait.Until(t, "the deferred wake to reach the receiver once the lock was released", func() bool {
		return notified.Load()
	})
}

// The ordinary arm keeps its immediate wake, and the wake runs to completion
// before the caller returns. Without this, the fix above could be "never wake",
// which would strand every ordinary delivery's receiver.
func TestOrdinaryArmStillWakesImmediately(t *testing.T) {
	receiver := &Session{id: "child-receiver"}
	var notified atomic.Bool
	receiver.notifyFunc = func() { notified.Store(true) }

	receiver.wakeAfterDelegateArm(noBound)
	if !notified.Load() {
		t.Fatal("an arm that supplied no bound did not wake the receiver before returning: the deferred wake " +
			"is for the close join only, and the ordinary path must keep today's behavior exactly")
	}
}
