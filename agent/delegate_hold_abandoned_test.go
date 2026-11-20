package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// The drain's hold sample must agree with the drain's own liveness walk about
// an abandoned branch: treeHasOutstandingWorkBesidesOwnJobs skips a child the
// drain gave up on, so the "waiting on N subagents" count must not keep
// including it. The mark is generation-scoped, and a later revive clears it.
func TestDrainHoldSampleSkipsDrainAbandonedDescendant(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: delegatestore.State{},
		delegateChildren: map[string]map[string]struct{}{
			"hold-parent": {"hold-one": {}, "hold-two": {}},
		},
		live: map[string]*delegateLiveState{},
	}
	for _, id := range []string{"hold-one", "hold-two"} {
		aggregate := tallyAggregate(id, "", true)
		aggregate.Generation = 1
		aggregate.Descriptor.ParentDelegateID = "hold-parent"
		aggregate.Descriptor.ChildSessionID = "child-" + id[len("hold-"):]
		c.durable[id] = aggregate
	}
	c.durable["hold-parent"] = tallyAggregate("hold-parent", "", true)
	s := &Session{delegateController: c}
	s.drainHeld.Store(true)
	c.live["hold-parent"] = &delegateLiveState{runtime: s}

	if got := c.sampleDrainHoldLocked("hold-parent"); !got.heldSubagents || got.subagentCount != 2 {
		t.Fatalf("hold sample before any abandonment = %+v, want both descendants", got)
	}

	// One descendant is abandoned at its running generation: the wait is now
	// the sibling alone.
	s.recordDrainAbandonedChild("child-one", "hold-one", 1, "test")
	if got := c.sampleDrainHoldLocked("hold-parent"); !got.heldSubagents || got.subagentCount != 1 {
		t.Fatalf("hold sample after abandoning one descendant = %+v, want only the sibling", got)
	}
	// The stamp is generation-scoped: once the delegate starts a newer run, the
	// old stamp no longer speaks for it and its branch counts again.
	s.recordDrainAbandonedChild("child-two", "hold-two", 1, "test")
	if got := c.sampleDrainHoldLocked("hold-parent"); got.subagentCount != 0 || got.heldSubagents {
		t.Fatalf("hold sample after abandoning both descendants = %+v, want no subagent wait", got)
	}
	c.durable["hold-one"].Generation = 2
	if got := c.sampleDrainHoldLocked("hold-parent"); !got.heldSubagents || got.subagentCount != 1 {
		t.Fatalf("hold sample after a newer run under the abandoned delegate = %+v, want that branch back", got)
	}

	// A revive clears the stamp and both branches count again.
	s.clearDrainAbandonedDelegate("hold-one")
	s.clearDrainAbandonedDelegate("hold-two")
	if got := c.sampleDrainHoldLocked("hold-parent"); !got.heldSubagents || got.subagentCount != 2 {
		t.Fatalf("hold sample after the revive cleared the stamps = %+v, want both descendants", got)
	}
}

// The abandonment's publication must seed the ancestor join from the abandoned
// row itself: its immediate parent may be mid-turn with no hold of its own, and
// the parked grandparent's count would otherwise keep the stale branch until
// some unrelated frame corrected it.
func TestDrainAbandonmentPublishesTheAbandonedRowForTheAncestorJoin(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: delegatestore.State{},
		delegateChildren: map[string]map[string]struct{}{
			"hold-grandparent": {"hold-parent": {}},
			"hold-parent":      {"hold-child": {}},
		},
		live: map[string]*delegateLiveState{},
	}
	for _, id := range []string{"hold-parent", "hold-child"} {
		aggregate := tallyAggregate(id, "", true)
		aggregate.Generation = 1
		c.durable[id] = aggregate
	}
	c.durable["hold-parent"].Descriptor.ParentDelegateID = "hold-grandparent"
	c.durable["hold-parent"].Descriptor.ChildSessionID = "parent-session"
	c.durable["hold-child"].Descriptor.ParentDelegateID = "hold-parent"
	c.durable["hold-child"].Descriptor.ChildSessionID = "child-session"
	c.durable["hold-grandparent"] = tallyAggregate("hold-grandparent", "", true)

	grandparent := &Session{id: "grandparent-session"}
	grandparent.drainHeld.Store(true)
	c.live["hold-grandparent"] = &delegateLiveState{runtime: grandparent}
	// The immediate parent is mid-turn: no hold of its own to publish.
	parent := &Session{id: "parent-session", owningDelegateID: "hold-parent", delegateController: c}
	c.live["hold-parent"] = &delegateLiveState{runtime: parent}
	var published []delegateUpdatePlan
	c.emitUpdate = func(plan delegateUpdatePlan) {
		published = append(published, plan)
	}

	if got := c.sampleDrainHoldLocked("hold-grandparent"); !got.heldSubagents || got.subagentCount != 2 {
		t.Fatalf("hold sample before the abandonment = %+v, want both descendants", got)
	}
	parent.recordDrainAbandonedChild("child-session", "hold-child", 1, "test")
	if got := c.sampleDrainHoldLocked("hold-grandparent"); !got.heldSubagents || got.subagentCount != 1 {
		t.Fatalf("hold sample after the abandonment = %+v, want only the parent branch", got)
	}
	if len(published) != 1 || len(published[0].rows) != 1 || published[0].rows[0].id != "hold-child" {
		t.Fatalf("abandonment published %+v, want one plan seeded from the abandoned row", published)
	}
}
