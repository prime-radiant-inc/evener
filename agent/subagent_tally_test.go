package agent

import (
	"testing"
	"time"

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

// The status row's Terminal field follows delegateRunTerminal, the same rule
// the row tally and the Subagents list projection share, over all four
// combinations of outcome presence and run-open state.
func TestDelegateStatusInfoTerminalFollowsTheSharedRule(t *testing.T) {
	outcome := &delegatestore.Outcome{Status: delegatestore.OutcomeCompleted}
	for _, tc := range []struct {
		name           string
		lastOutcome    *delegatestore.Outcome
		currentRunOpen bool
		want           bool
	}{
		{"no outcome, run open", nil, true, false},
		{"no outcome, run not open", nil, false, false},
		{"outcome present, run open", outcome, true, false},
		{"outcome present, run not open", outcome, false, true},
	} {
		row := delegateSnapshot{id: "d1", lastOutcome: tc.lastOutcome, currentRunOpen: tc.currentRunOpen}
		got := delegateStatusInfoFromSnapshot(time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), "root", row).Terminal
		if shared := delegateRunTerminal(row.lastOutcome, row.currentRunOpen); got != shared {
			t.Errorf("%s: Terminal = %v, delegateRunTerminal = %v; want them equal", tc.name, got, shared)
		}
		if got != tc.want {
			t.Errorf("%s: Terminal = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The stop gate fires only for a delegate whose latest run ended terminal
// (delegateRunTerminal) with a stopped_by_parent outcome; every other
// combination of outcome and run-open state refuses it.
func TestDelegateRowStopGatedFollowsTheSharedTerminalRule(t *testing.T) {
	stoppedByParent := &delegatestore.Outcome{Status: delegatestore.OutcomeStopped, Reason: "stopped_by_parent"}
	for _, tc := range []struct {
		name           string
		lastOutcome    *delegatestore.Outcome
		currentRunOpen bool
		want           bool
	}{
		{"no outcome, no open run", nil, false, false},
		{"stopped by parent, run open", stoppedByParent, true, false},
		{"stopped by parent, no open run", stoppedByParent, false, true},
		{"stopped for another reason, no open run", &delegatestore.Outcome{Status: delegatestore.OutcomeStopped, Reason: "other"}, false, false},
		{"failed, no open run", &delegatestore.Outcome{Status: delegatestore.OutcomeFailed}, false, false},
	} {
		row := delegateSnapshot{id: "d1", lastOutcome: tc.lastOutcome, currentRunOpen: tc.currentRunOpen}
		if got := delegateRowStopGated(row); got != tc.want {
			t.Errorf("%s: delegateRowStopGated = %v, want %v", tc.name, got, tc.want)
		}
	}
}
