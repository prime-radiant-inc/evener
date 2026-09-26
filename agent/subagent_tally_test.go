package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// tallyAggregate is one folded delegate: its latest outcome ("" before any run
// ended) and whether a run is open after it.
func tallyAggregate(id string, status delegatestore.OutcomeStatus, runOpen bool) *delegatestore.Aggregate {
	aggregate := &delegatestore.Aggregate{DelegateID: id, Phase: delegatestore.PhaseIdle, Resumable: true, CurrentRunOpen: runOpen}
	if runOpen {
		aggregate.Phase = delegatestore.PhaseRunning
	}
	if status != "" {
		aggregate.LatestOutcome = &delegatestore.Outcome{Status: status}
	}
	return aggregate
}

// The row's tally counts every delegate in the tree, nested ones and ones past
// the row's children cap included, by how its latest run stands: running
// until a run has ended with none open after it, failed when that run ended
// failed or exhausted, done otherwise (spec 9, S3 ruling 21).
func TestSubagentTallyCountsEveryDelegateInTheTreeByState(t *testing.T) {
	controller := &delegateTreeController{durable: delegatestore.State{}}
	for _, aggregate := range []*delegatestore.Aggregate{
		tallyAggregate("d-first-run", "", true),
		tallyAggregate("d-created", "", false),
		tallyAggregate("d-resumed", delegatestore.OutcomeFailed, true),
		tallyAggregate("d-failed", delegatestore.OutcomeFailed, false),
		tallyAggregate("d-exhausted", delegatestore.OutcomeExhausted, false),
		tallyAggregate("d-nested-failed", delegatestore.OutcomeFailed, false),
		tallyAggregate("d-completed", delegatestore.OutcomeCompleted, false),
		tallyAggregate("d-cancelled", delegatestore.OutcomeCancelled, false),
		tallyAggregate("d-stopped", delegatestore.OutcomeStopped, false),
	} {
		controller.durable[aggregate.DelegateID] = aggregate
	}
	controller.durable["d-nested-failed"].Descriptor.ParentDelegateID = "d-completed"
	if got, want := controller.tally(), (appwire.SubagentTally{Running: 3, Failed: 3, Done: 3}); got != want {
		t.Fatalf("tally = %+v, want %+v", got, want)
	}
}

// Only the tree's root session owns the controller. A child session shares
// its root's controller and must not report the root's tree as its own; a
// session with no delegate tree reports none.
func TestSubagentTallyIsTheRootsAlone(t *testing.T) {
	controller := &delegateTreeController{durable: delegatestore.State{"d": tallyAggregate("d", "", true)}}
	for _, tc := range []struct {
		name    string
		session *Session
		want    bool
	}{
		{"the root", &Session{delegateController: controller, ownsDelegateController: true}, true},
		{"a child session", &Session{delegateController: controller}, false},
		{"no delegate tree", &Session{}, false},
	} {
		tally, ok := tc.session.SubagentTally()
		if ok != tc.want || (ok && tally.Running != 1) {
			t.Errorf("%s: SubagentTally = %+v, %v; want ok %v", tc.name, tally, ok, tc.want)
		}
	}
}
