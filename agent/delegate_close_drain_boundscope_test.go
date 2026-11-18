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
// The fixture design matters more than the assertion, because three earlier
// versions of this test were green for the wrong reason:
//
//   - It first captured both plans for the SAME delegate, so "an update arrived"
//     was satisfied by the other pass's publication.
//   - It then used one shared `parked` flag for both publishers, so it could free
//     the buffer before the spent-bound publisher had even reached its send (a
//     false red against correct code), and a regression could pass whenever the
//     other update was buffered but not yet recorded (a false green).
//   - It then ran the spent-bound publisher to COMPLETION before starting the
//     sibling. That established the right facts in the wrong window: a bound
//     shared only while a pass is ACTIVE was installed and cleared before the
//     sibling ever sent, so the regression this test exists for passed.
//
// Both plans now travel in ONE pass, so the sibling's send is attempted while
// that pass is still running. The order is established rather than hoped for:
// the spent plan returns, the sibling reaches backpressure with the buffer still
// full, and only then does the consumer resume.
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

	// The other plan's bound, already spent.
	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	otherPlan := controller.capturedPlanLocked(otherID)
	otherPlan.bound = spent

	// The sibling's own plan, produced on the session's ordinary path.
	siblingPlan := controller.capturedPlanLocked(siblingID)
	if siblingPlan.bound != nil {
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

	// The sibling's send is the only one that can park: the other plan's bound is
	// already spent, so its send gives up without waiting. One signal is
	// therefore unambiguous, and a pass that returned before the sibling sent
	// would leave it at zero.
	var parks atomic.Int32
	root.testOnlyBlockedSendEntered = func() { parks.Add(1) }
	t.Cleanup(func() { root.testOnlyBlockedSendEntered = nil })

	// The consumer is held back until the test releases it, so "the buffer was
	// still full" is a fact the assertion established rather than a race it won.
	resume := make(chan struct{})
	var resumeOnce sync.Once
	openResume := func() { resumeOnce.Do(func() { close(resume) }) }
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopReader := func() { stopOnce.Do(func() { close(stop) }) }
	var sawSibling, sawOther atomic.Bool
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		<-resume
		for {
			select {
			case <-stop:
				return
			case ev := <-root.events:
				update, isUpdate := ev.Data.(events.DelegateUpdatedData)
				if !isUpdate {
					continue
				}
				switch update.DelegateID {
				case siblingID:
					sawSibling.Store(true)
				case otherID:
					sawOther.Store(true)
				}
			}
		}
	}()
	// Teardown: let the reader run, let it take any still-parked send (a parked
	// send holds eventsMu.RLock, and a later Close needs that lock), then stop it.
	t.Cleanup(func() {
		openResume()
		stopReader()
		<-readerDone
	})

	// ONE pass, both plans, in order. The spent plan publishes first and returns
	// at once; the sibling's send is then attempted inside the same, still-
	// running pass.
	done := make(chan struct{})
	go func() {
		defer close(done)
		controller.emitDelegateUpdates(delegateMutationPlans{updates: []delegateUpdatePlan{otherPlan, siblingPlan}})
	}()

	testwait.Until(t, "the sibling's send to reach the blocked-send boundary", func() bool {
		return parks.Load() > 0
	})
	select {
	case <-done:
		// Two causes, and the fixture can tell them apart: the stream really was
		// full (asserted below), so a pass that returned without a park means the
		// sibling's send gave up -- it inherited a bound it never supplied, which
		// is the regression this test pins.
		t.Fatal("the pass returned without the sibling's send parking although the stream was full: " +
			"the sibling's send gave up on a bound its own plan never carried, so a bounded pass is " +
			"still setting the deadline for publications it does not own")
	default:
	}
	if got := len(root.events); got != 1 {
		t.Fatalf("the stream holds %d event(s) while the sibling is parked, want 1: a slot was free, so "+
			"the sibling's send proves nothing about its own bound", got)
	}
	if siblingID == otherID {
		t.Fatal("both plans name the same delegate: the sibling assertion needs a distinct row")
	}

	// Only now may the consumer resume. It must see the sibling's OWN row.
	openResume()
	testwait.Until(t, "the consumer to receive the sibling delegate's own update", sawSibling.Load)
	<-done

	// The stream is FIFO and the other plan sent first, so by the time the
	// sibling's row arrives any row the other plan managed to publish would
	// already have been read: this is not a race with a later send.
	if sawOther.Load() {
		t.Fatal("the other pass's own EXPIRED bound did not suppress its own update: this fixture never " +
			"applied a plan bound at all, so the sibling's arrival cannot distinguish an isolated bound " +
			"from no bound")
	}
}
