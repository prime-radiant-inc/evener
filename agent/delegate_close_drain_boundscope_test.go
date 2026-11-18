package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// A publication bound belongs to the pass that supplied it. It used to be
// installed on the controller shared with the live root, so a terminal child
// teardown's deadline applied to every delegate publication for as long as that
// teardown ran: an unrelated sibling finishing in that window had its update and
// activity invalidation dropped.
//
// The bound now travels on the plan, so a plan produced outside any pass carries
// none and takes the session's ordinary delivery path regardless of what another
// pass is doing.
//
// The fixture design matters more than the assertion, because two earlier
// versions of this test were green for the wrong reason:
//
//   - It first captured both plans for the SAME delegate, so "an update arrived"
//     was satisfied by the other pass's publication.
//   - It then used one shared `parked` flag for both publishers, so it could free
//     the buffer before the spent-bound publisher had even reached its send (a
//     false red against correct code), and a regression could pass whenever the
//     other update was buffered but not yet recorded (a false green).
//
// Each publisher now has its OWN signal, and each fact is established before the
// next step: the spent-bound publisher must have RETURNED while the buffer was
// still full, and only then does the consumer resume.
func TestPublicationBoundDoesNotReachAnUnrelatedSibling(t *testing.T) {
	r := newWorktreeRepo(t)
	// Two distinct delegates. A check for "a DelegateUpdatedData arrived" cannot
	// be satisfied by one plan when the assertion names the other's ID.
	siblingID, _, _ := r.seedIsolationLane(t)
	otherID, _, _ := r.seedIsolationLane(t)
	if siblingID == otherID {
		t.Fatal("fixture seeded one delegate twice: the sibling assertion needs a distinct row")
	}
	root := r.s
	controller := root.delegateController

	// The other pass's bound, already spent.
	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	otherPlan := controller.capturedPlanLocked(otherID)
	otherPlan.bound = spent

	// The sibling's own plan, produced on the session's ordinary path.
	plan := controller.capturedPlanLocked(siblingID)
	if plan.bound != nil {
		t.Fatal("a plan produced outside any pass carries a bound: the deadline would apply to a " +
			"caller that supplied none")
	}

	// The receiver must be CLOSING for any plan's bound to apply at all: see
	// emitDelegateUpdate's per-target rule. Without this both plans take the
	// ordinary unbounded path and the spent bound is never consulted.
	closing, cancelClosing := context.WithCancel(t.Context())
	root.closeCtxMu.Lock()
	root.closeCtx = closing
	root.closeCtxMu.Unlock()
	t.Cleanup(func() {
		root.closeCtxMu.Lock()
		root.closeCtx = nil
		root.closeCtxMu.Unlock()
		cancelClosing()
	})

	// A full authoritative stream: a send that needs a slot must park.
	root.events = make(chan events.SessionEvent, 1)
	root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	// DISTINCT signals, one per publisher. A shared flag cannot say which send
	// parked, and that is what made the earlier fixture both falsely red and
	// falsely green.
	var siblingParked, otherParked atomic.Bool
	var mu sync.Mutex
	root.testOnlyBlockedSendEntered = func() {
		mu.Lock()
		defer mu.Unlock()
		// The send that is parked right now belongs to whichever publisher has
		// not yet recorded its result. Both plans send the same DelegateUpdatedData
		// kind, so the flag is claimed by the publisher that is running: the
		// goroutine identity is the only thing that separates them, and each
		// publisher marks itself before calling.
		switch {
		case otherRunning.Load():
			otherParked.Store(true)
		default:
			siblingParked.Store(true)
		}
	}
	t.Cleanup(func() { root.testOnlyBlockedSendEntered = nil })

	// Run the spent-bound publisher FIRST and wait for it to finish, so the fact
	// "it gave up while the buffer was full" is established before anything can
	// free a slot. Sequential rather than concurrent, because the two facts this
	// test needs are ordered: the spent bound must lose while the stream is full,
	// and only then may the sibling's send find a free slot.
	otherRunning.Store(true)
	root.emitDelegateUpdate(otherPlan)
	otherRunning.Store(false)
	mu.Lock()
	otherDidPark := otherParked.Load()
	mu.Unlock()
	if !otherDidPark {
		t.Fatal("the spent-bound publication never reached the blocked-send boundary: it either took a " +
			"slot the fixture meant to be full or returned before sending, so this fixture never " +
			"applied a plan bound at all")
	}
	if got := len(root.events); got != 1 {
		t.Fatalf("the stream holds %d event(s) after the spent-bound publication, want 1: it took a "+
			"slot, so the sibling's delivery below would prove nothing about its own bound", got)
	}

	// Now the sibling publishes on its own plan. It must PARK -- its bound is
	// absent, so there is nothing to give up on -- and deliver once the consumer
	// resumes.
	siblingParked.Store(false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		root.emitDelegateUpdate(plan)
	}()
	testwait.Until(t, "the sibling's send to park on the full stream", siblingParked.Load)

	// The consumer resumes. It must see the sibling's OWN row.
	var mu2 sync.Mutex
	var sawSibling, sawOther bool
	consumerStop := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case <-consumerStop:
				return
			case ev := <-root.events:
				update, ok := ev.Data.(events.DelegateUpdatedData)
				if !ok {
					continue
				}
				mu2.Lock()
				switch update.DelegateID {
				case siblingID:
					sawSibling = true
				case otherID:
					sawOther = true
				}
				mu2.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		close(consumerStop)
		<-consumerDone
	})

	// The publisher returning is not the consumer having taken the event: the
	// send completes against the buffer. Wait for the consumer's own observation.
	testwait.Until(t, "the consumer to receive the sibling delegate's own update", func() bool {
		mu2.Lock()
		defer mu2.Unlock()
		return sawSibling
	})
	<-done

	mu2.Lock()
	defer mu2.Unlock()
	if sawOther {
		t.Fatal("the other pass's own EXPIRED bound did not suppress its own update: this fixture never " +
			"applied a plan bound at all, so the sibling's arrival cannot distinguish an isolated bound " +
			"from no bound")
	}
}

// otherRunning marks which publisher is mid-send, so the blocked-send hook can
// attribute the park to the right one. Both publishers send the same event kind
// through the same session, and the hook receives no identity of its own.
var otherRunning atomic.Bool
