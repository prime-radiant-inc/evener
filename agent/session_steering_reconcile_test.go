package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestReconcileClientSteeringTable walks every rule of reconcileClientSteering
// (see its doc comment) on a snapshot built for the row, and checks the one
// action the rule promises.
func TestReconcileClientSteeringTable(t *testing.T) {
	t.Parallel()
	const id, turn = "cm-steer", "turn_m7"
	build := func(pending, held bool) clientMutationSnapshot {
		s := newEmptyClientMutationSnapshot("s1")
		s.SteeringOrder = []string{id}
		s.SteeringHeld = held
		if pending {
			s.PendingExecutions[id] = appwire.PendingMutation{
				ClientMutationID: id,
				Method:           clientMutationMethodSteer,
				TurnID:           turn,
				ExecutionState:   "accepted",
				ProjectionState:  appwire.MutationProjectionPending,
			}
			s.Journal[id] = clientMutationRecord{ClientMutationID: id, Method: clientMutationMethodSteer, StableTurnID: turn, OperationState: clientMutationOperationApplied, ExecutionState: "accepted"}
		}
		return s
	}
	recorded := func(string) string { return "incorporated" }
	recordedFailed := func(string) string { return "failed" }
	unrecorded := func(string) string { return "" }
	cases := []struct {
		row      string
		pending  bool
		held     bool
		recorded func(string) string
		stopping bool
		// wantPending is whether the steer is still a pending execution after;
		// wantInOrder whether the order still names it; wantHeld the hold.
		wantPending bool
		wantInOrder bool
		wantHeld    bool
	}{
		{"0 accepted, recorded: finalized (the append landed, the mark did not)", true, false, recorded, false, false, false, false},
		{"0 accepted, recorded as a selection failure: retired as failed", true, false, recordedFailed, false, false, false, false},
		{"0 accepted, recorded, Stop: finalized, not parked", true, false, recorded, true, false, false, false},
		{"0 accepted, recorded, held, last steer: finalized and the hold released (H)", true, true, recorded, true, false, false, false},
		{"1 absent: stale order entry dropped", false, false, unrecorded, false, false, false, false},
		{"1 absent, held: dropped and the hold naming nothing released (H)", false, true, unrecorded, false, false, false, false},
		{"1 absent, Stop: dropped, nothing to park", false, false, unrecorded, true, false, false, false},
		{"2 accepted, Stop: parked", true, false, unrecorded, true, true, true, true},
		{"2 accepted, held, Stop: left parked", true, true, unrecorded, true, true, true, true},
		{"accepted, restore: left runnable, no arm", true, false, unrecorded, false, true, true, false},
		{"accepted, held, restore: left parked (release only, never arm)", true, true, unrecorded, false, true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.row, func(t *testing.T) {
			snapshot := build(tc.pending, tc.held)
			reconcileClientSteering(&snapshot, tc.recorded, tc.stopping)
			inOrder := false
			for _, o := range snapshot.SteeringOrder {
				inOrder = inOrder || o == id
			}
			_, pending := snapshot.PendingExecutions[id]
			if inOrder != tc.wantInOrder || pending != tc.wantPending || snapshot.SteeringHeld != tc.wantHeld {
				t.Fatalf("inOrder=%v pending=%v held=%v, want inOrder=%v pending=%v held=%v",
					inOrder, pending, snapshot.SteeringHeld, tc.wantInOrder, tc.wantPending, tc.wantHeld)
			}
			if tc.pending && !tc.wantPending {
				if snapshot.Journal[id].ExecutionState != tc.recorded(id) || snapshot.Journal[id].OperationState != clientMutationOperationTerminal {
					t.Fatalf("retired steer's journal = %q/%q, want terminal/%s", snapshot.Journal[id].OperationState, snapshot.Journal[id].ExecutionState, tc.recorded(id))
				}
			}
		})
	}
}

// TestLoadReleasesASlotOnlyAUserTurnOwns is the load-time slot sweep
// (forgetRunningTurnNoOneOwns): a pending turn/start or turn/queue naming the
// active turn keeps it -- restore re-runs that turn by its id -- and any other
// name is an orphan, a steering carrier's claim included (#1342).
func TestLoadReleasesASlotOnlyAUserTurnOwns(t *testing.T) {
	t.Parallel()
	const turn = "turn_m7"
	build := func(method string) clientMutationSnapshot {
		s := newEmptyClientMutationSnapshot("s1")
		s.ActiveTurnID = turn
		if method != "" {
			s.PendingExecutions["cm"] = appwire.PendingMutation{ClientMutationID: "cm", Method: method, TurnID: turn, ExecutionState: "accepted"}
			if method == clientMutationMethodSteer {
				s.SteeringOrder = []string{"cm"}
			}
		}
		return s
	}
	cases := []struct {
		name     string
		method   string
		wantKept bool
	}{
		{"no pending execution names it: released", "", false},
		{"a pending turn/start names it: kept for restore to re-run", clientMutationMethodStart, true},
		{"a pending turn/queue names it: kept", clientMutationMethodQueue, true},
		{"a pending steer names it (a carrier claim the process died under): released", clientMutationMethodSteer, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := build(tc.method)
			forgetRunningTurnNoOneOwns(&snapshot)
			if kept := snapshot.ActiveTurnID == turn; kept != tc.wantKept {
				t.Fatalf("ActiveTurnID=%q, want kept=%v", snapshot.ActiveTurnID, tc.wantKept)
			}
		})
	}
}
