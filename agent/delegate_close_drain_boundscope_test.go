package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/internal/testwait"
)

// siblingPublication reports whether data is one of the publications the named
// sibling's plan makes: its own row update, or the activity invalidation that
// precedes it.
func siblingPublication(data events.EventData, siblingID string) bool {
	switch typed := data.(type) {
	case events.DelegateUpdatedData:
		return typed.DelegateID == siblingID
	case events.SessionActivityChangedData:
		return true
	default:
		return false
	}
}

// siblingEmitting marks the publication window of the SIBLING's plan alone, so
// the blocked-send hook can attribute a park to it. A spent plan enters the same
// hook before it consults its bound; only the sibling's window counts.
var siblingEmitting atomic.Bool

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
// The fixture design matters more than the assertion, because four earlier
// versions of this test were green for the wrong reason:
//
//   - It first captured both plans for the SAME delegate, so "an update arrived"
//     was satisfied by the other pass's publication.
//   - It then used one shared `parked` flag for both publishers, so it could free
//     the buffer before the spent-bound publisher had even reached its send (a
//     false red against correct code), and a regression could pass whenever the
//     other update was buffered but not yet recorded (a false green).
//   - It then ran the spent-bound publisher to COMPLETION before starting the
//     sibling, so a bound shared only while a pass is ACTIVE was installed and
//     cleared before the sibling ever sent.
//   - It then hand-built one emitDelegateUpdates batch. That exercises a batch,
//     not a DRAIN: the regression this test exists for was installed by
//     drainStopAbandonable on the shared controller, so a mutant reinstalled
//     there stayed green.
//
// The real drain is pinned by TestSiblingUpdateSurvivesAnInFlightBoundedDrain
// below, which holds an actual drainStopAbandonable in flight. This test pins
// the batch boundary: both plans in ONE pass, so the sibling's send is attempted
// while that pass is still running.
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

	// A sibling-ONLY park signal, attributed to the plan actually being emitted.
	//
	// A flag set for the whole batch is not attribution: emitDelegateUpdates runs
	// the plans in order, so while the SPENT plan is parked the sibling flag is
	// already set, and the spent plan's own park would be read as the sibling's.
	// Releasing the reader there frees a slot the spent plan then takes -- a
	// failure against correct code. The marker is therefore set around the
	// sibling's plan alone, by a publisher that names it.
	var siblingParked atomic.Bool
	root.testOnlyBlockedSendEntered = func() {
		if siblingEmitting.Load() {
			siblingParked.Store(true)
		}
	}
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
	t.Cleanup(func() {
		openResume()
		stopReader()
		awaitCloseFenceSignal(t, readerDone, "the fixture's consumer loop stopping")
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Emit in order, marking only the sibling's own publication. This is the
		// same path emitDelegateUpdates takes, with the attribution the batch
		// helper cannot carry.
		controller.emitDelegateUpdate(otherPlan)
		siblingEmitting.Store(true)
		defer siblingEmitting.Store(false)
		controller.emitDelegateUpdate(siblingPlan)
	}()
	// Join the publisher on EVERY path, including an assertion failure above.
	// Releasing the consumer and restoring the fixture while this goroutine is
	// still parked would let it take a slot in a stream the test already
	// finished measuring, and the failure it reported would name the wrong fact.
	// Cleanups run last-registered-first, so this join happens before the
	// consumer loop is released and stopped below.
	t.Cleanup(func() {
		openResume()
		awaitCloseFenceSignal(t, done, "the fixture's publisher returning once the consumer was released")
	})

	testwait.Until(t, "the sibling's own send to reach the blocked-send boundary", siblingParked.Load)
	select {
	case <-done:
		// The stream really was full (asserted next), so a pass that returned
		// without the sibling parking means the sibling's send gave up: it
		// inherited a bound its own plan never carried.
		t.Fatal("the pass returned without the sibling's send parking although the stream was full: " +
			"the sibling's send gave up on a bound its own plan never carried, so a bounded pass is " +
			"still setting the deadline for publications it does not own")
	default:
	}
	if got := len(root.events); got != 1 {
		t.Fatalf("the stream holds %d event(s) while the sibling is parked, want 1: a slot was free, so "+
			"the sibling's send proves nothing about its own bound", got)
	}

	// Only now may the consumer resume. It must see the sibling's OWN row.
	openResume()
	testwait.Until(t, "the consumer to receive the sibling delegate's own update", sawSibling.Load)
	// JOIN before restoring any fixture state: the publisher outlives the
	// observation above, and letting it run on during teardown is how a later
	// assertion samples a half-restored world.
	awaitCloseFenceSignal(t, done, "the publishing pass returning once the consumer freed the slot")

	// The stream is FIFO and the other plan sent first, so by the time the
	// sibling's row arrives any row the other plan managed to publish would
	// already have been read: this is not a race with a later send.
	if sawOther.Load() {
		t.Fatal("the other pass's own EXPIRED bound did not suppress its own update: this fixture never " +
			"applied a plan bound at all, so the sibling's arrival cannot distinguish an isolated bound " +
			"from no bound")
	}
}

// The regression this test exists for lived at the DRAIN boundary:
// drainStopAbandonable installed its publication cutoff on the controller shared
// with the live root. A hand-built batch cannot see that, so this fixture holds a
// REAL drain in flight and publishes an unrelated sibling through its ordinary
// entry point while the drain's own publication window is already spent.
//
// The drain's publication context is spent without stopping the drain: the
// cascade cutoff is inherited through closeCascadePublicationKey, so a long-past
// value there expires the publications while the drain's reconciliation waits
// keep their live context. That makes the drain's bound genuinely spent at the
// moment the sibling publishes, which is the condition that used to suppress it.
//
// If the drain leaks that bound onto the controller, the sibling's send to the
// closing root gives up at once and never delivers. If the bound stays with the
// pass that supplied it, the sibling takes the ordinary unbounded path, parks on
// the full stream, and delivers its own row once the consumer resumes.
func TestSiblingUpdateSurvivesAnInFlightBoundedDrain(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{
		updates: 1, retryable: true, topLevel: true, realRoot: true, otherPending: true,
	})
	root := f.root
	if root == nil {
		t.Fatal("fixture has no real root session")
	}
	c := f.controller
	targetDelivery := f.deliveries[0].deliveryID
	// The harness controller has no publisher: production wires emitUpdate to the
	// root session's own publisher (attachDelegateController). Without this the
	// sibling's plan would be dropped in emitDelegateUpdate and never reach a
	// send at all, so the fixture would be green for the wrong reason.
	c.mu.Lock()
	c.emitUpdate = root.emitDelegateUpdate
	c.mu.Unlock()

	// Park the drain on a transient acknowledgment failure: the first attempt
	// fails the append and the drain waits for its re-armed retry. Nothing about
	// the drain is faked -- this is the same park the retry path uses.
	var failed atomic.Bool
	c.mu.Lock()
	c.appendFault = func(appended []delegatestore.Event) error {
		if !delegateEventsAcknowledgeDelivery(appended, targetDelivery) {
			return nil
		}
		failed.Store(true)
		return errors.New("test: drain acknowledgment append failure")
	}
	c.mu.Unlock()
	parked := make(chan struct{})
	var parkOnce sync.Once
	waitHook := func() {
		if failed.Load() {
			parkOnce.Do(func() { close(parked) })
		}
	}
	savedWait := observeDelegateStopWait.Load()
	observeDelegateStopWait.Store(&waitHook)
	t.Cleanup(func() { observeDelegateStopWait.Store(savedWait) })

	// The sibling's own plan, produced outside any pass.
	siblingID := "dlg_other"
	siblingPlan := c.capturedPlanLocked(siblingID)
	if siblingPlan.bound != nil {
		t.Fatal("a plan produced outside any pass carries a bound")
	}

	// The root is closing, so a plan bound WOULD apply to it: that is what makes
	// a leaked controller bound observable rather than inert.
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

	// A full authoritative stream, with the consumer held back.
	root.events = make(chan events.SessionEvent, 1)
	root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	// Every publication the sibling's plan makes is recorded WITH the context it
	// was about to be sent on, and checked while the stream is still full.
	//
	// Entered alone is not enough, and neither is watching only the row update.
	// The activity invalidation that precedes the row is published first and
	// takes the last slot, so the row's own send never even begins while the
	// buffer stays full: a fixture that waited for the row's park would time out,
	// and one that released the reader on WHATEVER parked first would let the row
	// take a freed slot while still carrying the drain's spent cutoff -- green
	// with the regression intact.
	//
	// Both publications come from the same plan bound and the same per-target
	// decision in emitDelegateUpdate, so checking the contexts of all of them is
	// checking the thing the leak would change.
	var (
		observedMu   sync.Mutex
		observedCtxs []context.Context
	)
	root.testOnlyBlockedSendObserved = func(data events.EventData, ctx context.Context) {
		if !siblingPublication(data, siblingID) {
			return
		}
		observedMu.Lock()
		observedCtxs = append(observedCtxs, ctx)
		observedMu.Unlock()
	}
	t.Cleanup(func() {
		root.testOnlyBlockedSendObserved = nil
		root.testOnlyBlockedSendEntered = nil
	})

	resume := make(chan struct{})
	var resumeOnce sync.Once
	openResume := func() { resumeOnce.Do(func() { close(resume) }) }
	stop := make(chan struct{})
	var stopOnce sync.Once
	var sawSibling atomic.Bool
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		<-resume
		for {
			select {
			case <-stop:
				return
			case ev := <-root.events:
				if update, ok := ev.Data.(events.DelegateUpdatedData); ok && update.DelegateID == siblingID {
					sawSibling.Store(true)
				}
			}
		}
	}()
	t.Cleanup(func() {
		openResume()
		stopOnce.Do(func() { close(stop) })
		awaitCloseFenceSignal(t, readerDone, "the fixture's consumer loop stopping")
	})

	// The drain's publication window, spent before the drain starts, while the
	// drain's own waits stay live.
	drainCtx, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	pubSpent := context.WithValue(drainCtx, closeCascadePublicationKey{}, time.Now().Add(-time.Hour))

	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		_ = c.drainStopAbandonable(pubSpent, c.stopForResult(f.stop), root, nil, true)
	}()
	awaitCloseFenceSignal(t, parked, "the real drain parking on stop progress")

	// The sibling publishes on the session's ordinary path while the drain runs.
	siblingDone := make(chan struct{})
	go func() {
		defer close(siblingDone)
		_ = root.executeDelegateMutationPlans(delegateMutationPlans{
			updates: []delegateUpdatePlan{siblingPlan},
		})
	}()
	testwait.Until(t, "the sibling's publication to reach the blocked-send boundary during the real drain",
		func() bool {
			observedMu.Lock()
			defer observedMu.Unlock()
			return len(observedCtxs) > 0
		})
	select {
	case <-siblingDone:
		t.Fatal("the sibling's send returned while a bounded drain was in flight: it was given up on a " +
			"bound its own plan never carried, so the drain is still setting the deadline for " +
			"publications it does not own")
	default:
	}
	if got := len(root.events); got != 1 {
		t.Fatalf("the stream holds %d event(s) while the sibling is parked, want 1: a slot was free, so "+
			"the sibling's send proves nothing about the drain's bound", got)
	}
	// The effective contexts, checked while the stream is still full and the
	// drain is still in flight: if the drain's spent cutoff reached this plan's
	// publications, it shows HERE, before any slot is freed.
	observedMu.Lock()
	contexts := append([]context.Context(nil), observedCtxs...)
	observedMu.Unlock()
	if len(contexts) == 0 {
		t.Fatal("the sibling published nothing: the fixture never reached the sends it pins")
	}
	for _, ctx := range contexts {
		if ds, marked := publicationBound(ctx); marked {
			if _, live := ds.Deadline(); !live {
				t.Fatal("a sibling publication inherited a bound with no deadline left: the drain's " +
					"spent cutoff reached a publication its own plan never carried")
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("a sibling publication arrived on an already-cancelled context: the drain's spent " +
				"cutoff reached a publication its own plan never carried")
		default:
		}
	}

	// The consumer resumes and the sibling's OWN row must arrive.
	openResume()
	testwait.Until(t, "the consumer to receive the sibling's own update during the real drain",
		sawSibling.Load)
	awaitCloseFenceSignal(t, siblingDone, "the sibling's send returning once the slot freed")

	// End the drain and join it before the fixture is torn down.
	cancelDrain()
	awaitCloseFenceSignal(t, drainDone, "the real drain returning after cancellation")
}
