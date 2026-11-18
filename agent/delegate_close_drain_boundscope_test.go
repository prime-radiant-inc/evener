package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// A publication bound belongs to the pass that supplied it. It used to be
// installed on the controller shared with the live root, so a terminal child
// teardown's deadline applied to every delegate publication for as long as that
// teardown ran: an unrelated sibling finishing in that window had its update and
// activity invalidation dropped, and overlapping passes could restore an already
// expired context if they finished out of order.
//
// The bound now travels on the plan, so a plan produced outside any pass carries
// none and takes the session's ordinary delivery path regardless of what another
// pass is doing.
//
// The discriminator is a send that must WAIT. The stream starts full and the
// consumer resumes only after the send has parked: an ordinary publication waits
// for the consumer and then delivers its update, while a plan carrying another
// pass's spent bound gives up while the stream is still full and drops it.
func TestPublicationBoundDoesNotReachAnUnrelatedSibling(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, _, _ := r.seedIsolationLane(t)
	root := r.s
	controller := root.delegateController

	// An unrelated pass holds a bound, and it is genuinely in use: a plan from
	// that other pass publishes against the same controller's target while this
	// one is under test. The spent result is checked below -- the point of
	// running it is that the sibling's own send must be unaffected by a
	// publication in flight, which a test that merely constructs a context and
	// discards it never exercises.
	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	otherPlan := controller.capturedPlanLocked(delegateID)
	otherPlan.bound = spent

	// The sibling's own plan, produced on the session's ordinary path.
	plan := controller.capturedPlanLocked(delegateID)
	if plan.bound != nil {
		t.Fatal("a plan produced outside any pass carries a bound: the deadline would apply to a caller that supplied none")
	}

	// A full authoritative stream: every send below parks until a slot frees.
	root.events = make(chan events.SessionEvent, 1)
	root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	var parked bool
	var parkedMu sync.Mutex
	root.testOnlyBlockedSendEntered = func() {
		parkedMu.Lock()
		parked = true
		parkedMu.Unlock()
	}
	t.Cleanup(func() { root.testOnlyBlockedSendEntered = nil })

	// The other pass publishes concurrently, on its spent bound. Its send must
	// give up rather than take the slot the sibling needs; the sibling's must
	// still park and then deliver. Both run at once, so this is an overlapping
	// publication rather than a bound that was merely constructed.
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		root.emitDelegateUpdate(otherPlan)
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		root.emitDelegateUpdate(plan)
	}()

	// It must be parked, not returned: this is a send that has to wait.
	testwait.Until(t, "the sibling's send to park on the full stream", func() bool {
		parkedMu.Lock()
		defer parkedMu.Unlock()
		return parked
	})

	// The consumer resumes. A publication on the ordinary path now takes the
	// freed slots; one carrying another pass's spent bound has already given up.
	var mu sync.Mutex
	var sawUpdate bool
	delivered := make(chan struct{})
	var once sync.Once
	stopConsumer := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case <-stopConsumer:
				return
			case ev := <-root.events:
				if _, ok := ev.Data.(events.DelegateUpdatedData); ok {
					mu.Lock()
					sawUpdate = true
					mu.Unlock()
					// Acknowledge the receipt itself. The publisher returning is
					// not the same event as this consumer having taken the
					// update: the send can complete against the buffer while
					// this goroutine is still to be scheduled, which made a
					// correct run fail.
					once.Do(func() { close(delivered) })
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(stopConsumer)
		<-consumerDone
	})

	// Wait for the consumer's own acknowledgement, not for the publisher.
	testwait.Until(t, "the consumer to receive the sibling's update", func() bool {
		select {
		case <-delivered:
			return true
		default:
			return false
		}
	})
	// The publisher must also have finished, so nothing is left in flight.
	<-done
	// The other pass's bounded send is finished too. It was spent from the
	// start, so it returned without delivering -- which is what makes the
	// sibling's delivery evidence of a per-plan bound rather than of an idle
	// controller.
	<-otherDone
	mu.Lock()
	got := sawUpdate
	mu.Unlock()
	if !got {
		t.Fatal("the sibling's update never reached the stream: a bound from an unrelated pass suppressed it")
	}
}
