package agent

import (
	"context"
	"errors"
	"testing"
)

// An ordinary stop driver must NOT stamp its cancellation onto the plans it
// returns.
//
// The driver's context is the join's abandon signal. A plan, though, is published
// by a later caller INTO a receiving session that never supplied that deadline:
// when the driver's cancellation dropped a committed update, the activity view it
// invalidated stayed open even though the consumer had resumed well inside the
// root's own delivery window. Only a close drain, whose target is the closing root
// sharing its budget, narrows a plan's send.
//
// The fixture drives a REAL settled stop through Reconcile, so the plans checked
// are the ones a stop actually produces rather than a hand-built batch.
func TestOrdinaryStopPassPlansCarryNoPublicationBound(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, topLevel: true})
	c := f.controller

	// The ordinary driver's own context: live, and cancellation-only in
	// production. The pass still supplies it as its WAIT bound.
	driverCtx, cancelDriver := context.WithCancel(t.Context())
	t.Cleanup(cancelDriver)

	evidence := emptyDelegateReconcileEvidence(c)
	evidence.bound = withPublicationBound(driverCtx)
	// planBound stays nil: this is the ordinary driver, and an ordinary driver
	// supplied no publication deadline for the plans it returns.
	plans, err := c.Reconcile(evidence)
	if err != nil && !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("reconcile: %v", err)
	}
	if len(plans.updates) == 0 {
		t.Fatalf("the fixture's settled stop produced no update plan (err=%v): nothing observed whether an "+
			"ordinary pass narrows its plans", err)
	}
	for i, plan := range plans.updates {
		if plan.bound != nil {
			t.Fatalf("update %d carries a publication bound: an ordinary stop pass supplied none, so the "+
				"receiving session's own delivery window must govern it", i)
		}
	}
}

// The close drain DOES narrow its plans, and to a context carrying the close-join
// mark the wake gate reads. Without this, the fix above could be "never stamp a
// plan", which would put the closure news back on the root's full close budget --
// the stall this whole change removes.
func TestCloseDrainPassPlansCarryItsPublicationBound(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, topLevel: true})
	c := f.controller

	driverCtx, cancelDriver := context.WithCancel(t.Context())
	t.Cleanup(cancelDriver)
	published := closeDrainBound(driverCtx)

	evidence := emptyDelegateReconcileEvidence(c)
	evidence.bound = published
	evidence.planBound = published
	plans, err := c.Reconcile(evidence)
	if err != nil && !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("reconcile: %v", err)
	}
	if len(plans.updates) == 0 {
		t.Fatalf("the fixture's settled stop produced no update plan (err=%v): nothing observed whether a "+
			"close drain narrows its plans", err)
	}
	for i, plan := range plans.updates {
		if plan.bound == nil {
			t.Fatalf("update %d carries no publication bound: a close drain's plan must release on the "+
				"cascade cutoff, not on the root's full close budget", i)
		}
		if !closeJoinBound(plan.bound) {
			t.Fatalf("update %d carries a bound without the close-join mark: the wake gate reads that mark, "+
				"so the plan would not be treated as close-join work", i)
		}
	}
}
