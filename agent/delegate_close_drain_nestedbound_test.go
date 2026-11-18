package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/internal/testwait"
)

// A resident delegate owns a collectible lane of its own, and its disposal pass
// runs before the root's. The announcement of its durable closure reaches the
// root's stream through emitDelegateUpdate. On the session's own path that send
// waits for the full close budget, so a stalled authoritative root stream lets a
// nested pass take the slots the root's own removals need -- and the root's
// collectible lanes stay on disk for a later prune.
//
// The announcement belongs to the pass that caused it, so it is published on that
// pass's bound: a consumer that can accept it still receives it, and one that
// cannot releases the pass.
func TestNestedClosureAnnouncementUsesTheDrainBound(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, _, _ := r.seedIsolationLane(t)
	root := r.s
	controller := root.delegateController

	// A stalled authoritative stream: the announcement can be delivered to a
	// consumer that resumes, or released by a bound.
	root.events = make(chan events.SessionEvent, 1)
	root.events <- events.New(events.HookStartData{Event: "occupies the only slot"})
	root.authoritativeConsumer = true
	t.Cleanup(func() { root.authoritativeConsumer = false })

	// A bound the pass supplied, already spent.
	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()
	restore := controller.setPublicationBound(bound)
	t.Cleanup(restore)

	var parked atomic.Bool
	root.testOnlyBlockedSendEntered = func() { parked.Store(true) }
	t.Cleanup(func() { root.testOnlyBlockedSendEntered = nil })

	plan := controller.capturedPlanLocked(delegateID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		root.emitDelegateUpdate(plan)
	}()

	// It must return on the pass's bound rather than park on the root's stream.
	testwait.Until(t, "the nested closure announcement to release on the drain's bound", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	if !parked.Load() {
		t.Fatal("the announcement never reached the blocked-send boundary: the send was not the thing under test")
	}
}
