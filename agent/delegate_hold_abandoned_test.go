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
		c.durable[id] = aggregate
	}
	c.durable["hold-parent"] = tallyAggregate("hold-parent", "", true)
	s := &Session{id: "hold-parent-session", owningDelegateID: "hold-parent", delegateController: c}
	s.drainHeld.Store(true)
	c.live["hold-parent"] = &delegateLiveState{runtime: s}
	var published []delegateUpdatePlan
	c.emitUpdate = func(plan delegateUpdatePlan) {
		published = append(published, plan)
	}

	if got := c.sampleDrainHoldLocked("hold-parent"); !got.heldSubagents || got.subagentCount != 2 {
		t.Fatalf("hold sample before any abandonment = %+v, want both descendants", got)
	}

	// One descendant is abandoned at its running generation: the wait is now
	// the sibling alone.
	s.recordDrainAbandonedChild("child-one", "hold-one", 1, "test")
	if got := c.sampleDrainHoldLocked("hold-parent"); !got.heldSubagents || got.subagentCount != 1 {
		t.Fatalf("hold sample after abandoning one descendant = %+v, want only the sibling", got)
	}
	// The count the row shows changed and the abandonment appends no delegate
	// event, so the parked parent's own row is republished.
	if len(published) != 1 || len(published[0].rows) != 1 || published[0].rows[0].id != "hold-parent" {
		t.Fatalf("abandonment published %+v, want one plan carrying the parked parent row", published)
	}

	// The stamp names a generation. A newer generation under the same delegate
	// is not the abandoned one, so its branch still counts.
	s.recordDrainAbandonedChild("child-two", "hold-two", 2, "test")
	if got := c.sampleDrainHoldLocked("hold-parent"); !got.heldSubagents || got.subagentCount != 1 {
		t.Fatalf("hold sample after stamping a newer generation = %+v, want the abandoned branch still skipped", got)
	}

	// A revive clears the stamp and both branches count again.
	s.clearDrainAbandonedDelegate("hold-one")
	s.clearDrainAbandonedDelegate("hold-two")
	if got := c.sampleDrainHoldLocked("hold-parent"); !got.heldSubagents || got.subagentCount != 2 {
		t.Fatalf("hold sample after the revive cleared the stamps = %+v, want both descendants", got)
	}
}
