package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// The publication bound a plan carries belongs to the pass that PRODUCED it, so
// it may only narrow a send to a session that supplied a bound of its own.
//
// The defect: an explicit worktree disposal can teardown a resident delegate and
// collect its nested lanes while the root stays live. The disposal pass stamps
// the child's stop-join bound onto the closure announcements, and
// emitDelegateUpdate applied that bound to the LIVE root's stream. A root stream
// that was full for the length of that bound then dropped both the closure
// update and its activity invalidation -- after the lane's removal had already
// run and its durable closure was already recorded. An open activity view kept
// showing a collected delegate as resumable until the next full read.
//
// The discriminating send is one that must WAIT: the root's stream starts full,
// the consumer resumes well inside the root's own close window, and the update
// must arrive. Bounding the root to the child's stamp drops it instead.
//
// The root here is deliberately NOT closing. That is the whole point of the
// finding -- the producing pass's deadline has no removals to protect on a live
// root -- and it is why a fixture that closes both sessions cannot reproduce it.
func TestLiveRootKeepsAResidentTeardownsClosureUpdate(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, _, _ := r.seedIsolationLane(t)
	root := r.s
	controller := root.delegateController

	// The pass's bound, as delegate_tree_stop.go stamps it: the stop-join half of
	// a cascade. The root never supplied this; only the closing pass did.
	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()

	plan := controller.capturedPlanLocked(delegateID)
	plan.bound = spent

	// A full authoritative stream on the LIVE root: the closure send parks.
	root.events = make(chan events.SessionEvent, 1)
	root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	// Guard the fixture's premise: the root is live, so it has published no
	// close budget for the pass's bound to legitimately narrow.
	root.closeCtxMu.RLock()
	rootClosing := root.closeCtx != nil
	root.closeCtxMu.RUnlock()
	if rootClosing {
		t.Fatal("fixture roots this session in a close; the finding is about a LIVE root")
	}

	var parked bool
	var parkedMu sync.Mutex
	root.testOnlyBlockedSendEntered = func() {
		parkedMu.Lock()
		parked = true
		parkedMu.Unlock()
	}
	t.Cleanup(func() { root.testOnlyBlockedSendEntered = nil })

	done := make(chan struct{})
	go func() {
		defer close(done)
		root.emitDelegateUpdate(plan)
	}()

	// It must actually park, or the spent bound never has to bite and the test
	// would pass with the defect intact.
	testwait.Until(t, "the closure send to park on the full root stream", func() bool {
		parkedMu.Lock()
		defer parkedMu.Unlock()
		return parked
	})

	// The consumer resumes, as it does whenever the stream drains in time.
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
					// Acknowledge the receipt itself: the publisher returning is
					// not the same event as this consumer having taken the update.
					once.Do(func() { close(delivered) })
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(stopConsumer)
		<-consumerDone
	})

	testwait.Until(t, "the root's consumer to receive the collected lane's update", func() bool {
		select {
		case <-delivered:
			return true
		default:
			return false
		}
	})
	<-done
}

// The other half of the same rule: a bound the RECEIVING session did supply is
// still honoured. A root that is itself closing shares its stream with the
// remaining removals, so a closure send there must not park on the full close
// budget waiting for a consumer that may never resume.
func TestClosingRootStillBoundsAResidentTeardownsClosureUpdate(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, _, _ := r.seedIsolationLane(t)
	root := r.s
	controller := root.delegateController

	spent, cancelSpent := context.WithCancel(t.Context())
	cancelSpent()
	plan := controller.capturedPlanLocked(delegateID)
	plan.bound = spent

	root.events = make(chan events.SessionEvent, 1)
	root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	// Publish a close budget: this root IS closing, so the pass's bound applies.
	closeBudget, cancelClose := context.WithCancel(t.Context())
	root.closeCtxMu.Lock()
	root.closeCtx = closeBudget
	root.closeCtxMu.Unlock()
	t.Cleanup(func() {
		root.closeCtxMu.Lock()
		root.closeCtx = nil
		root.closeCtxMu.Unlock()
		cancelClose()
	})

	// The send must return rather than park until the close deadline: the bound
	// is spent, and a closing root supplies it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		root.emitDelegateUpdate(plan)
	}()
	<-done
}
