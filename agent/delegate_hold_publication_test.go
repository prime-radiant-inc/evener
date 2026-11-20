package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A delegate with no drain hold outstanding must not
// publish a hold-only update when it enters or leaves its drain with nothing to
// hold on: the row cannot move, so the emit only makes an open activity view
// reread it. Once the controller has observed a hold, leaving the drain must
// still publish the clear so a loaded row retracts the wait.
func TestDelegateHoldPublicationSkipsClearSampleWithNoOutstandingHold(t *testing.T) {
	t.Parallel()
	tree := newRealDelegateTree(t, nil)
	tree.s.sendersWG.Wait()
	c := tree.c
	parentID := tree.parent.DelegateID
	runtimeID := tree.parentRuntime.ID()

	// Nothing holds this delegate, and nothing ever did: the publication the
	// drain entry and exit make must be a no-op.
	c.mu.Lock()
	delete(c.holdSamples, parentID)
	delete(c.publishedHoldSamples, parentID)
	c.mu.Unlock()
	drainPendingEvents(tree.s)
	c.publishDelegateHold(runtimeID, parentID)
	if got := drainRootDelegateUpdates(tree.s, parentID); len(got) != 0 {
		t.Fatalf("a clear hold with no outstanding hold published %d update(s): %+v", len(got), got)
	}

	// A hold a read handed out is outstanding. The read must not be the reason
	// a later drain exit stays silent: a row loaded during the read carries the
	// wait, and only the clear publication retracts it.
	c.mu.Lock()
	if _, err := c.appendLocked(delegateControllerRunStartedEvent(tree.grandchildID, 2, delegatestore.TriggerOwnerInput, time.Now().UTC())); err != nil {
		c.mu.Unlock()
		t.Fatalf("reopen grandchild: %v", err)
	}
	tree.parentRuntime.drainHeld.Store(true)
	sample, _ := c.drainHoldForDelegateLocked(parentID)
	c.mu.Unlock()
	if !sample.heldSubagents || sample.subagentCount != 1 {
		t.Fatalf("sampled hold = %+v, want the reopened grandchild", sample)
	}
	drainPendingEvents(tree.s)
	tree.parentRuntime.drainHeld.Store(false)
	c.publishDelegateHold(runtimeID, parentID)
	cleared := drainRootDelegateUpdates(tree.s, parentID)
	if len(cleared) != 1 {
		t.Fatalf("leaving a drain whose hold the controller sampled published %d update(s), want the clear: %+v",
			len(cleared), cleared)
	}
	if cleared[0].DrainHeldBySubagents || cleared[0].RunningSubagentCount != 0 {
		t.Fatalf("clear update still reports the hold: %+v", cleared[0])
	}
}
