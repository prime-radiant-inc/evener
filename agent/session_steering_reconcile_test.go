package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestReconcileClientSteeringTable walks every row of reconcileClientSteering's
// table (see its doc comment) plus the active-turn slot rule, on a snapshot
// built for the row, and checks the one action the row promises.
func TestReconcileClientSteeringTable(t *testing.T) {
	const id, turn = "cm-steer", "turn_m7"
	build := func(state string, held bool, active string) clientMutationSnapshot {
		s := newEmptyClientMutationSnapshot("s1")
		s.SteeringOrder = []string{id}
		s.SteeringHeld = held
		s.ActiveTurnID = active
		if state != "" {
			s.PendingExecutions[id] = appwire.PendingMutation{
				ClientMutationID: id,
				Method:           clientMutationMethodSteer,
				TurnID:           turn,
				ExecutionState:   state,
				ProjectionState:  appwire.MutationProjectionPending,
			}
			s.Journal[id] = clientMutationRecord{ClientMutationID: id, Method: clientMutationMethodSteer, StableTurnID: turn, OperationState: clientMutationOperationApplied, ExecutionState: state}
		}
		return s
	}
	recorded := func(string) bool { return true }
	unrecorded := func(string) bool { return false }
	type want struct {
		state   string // "" = gone from PendingExecutions
		inOrder bool
		held    bool
		rearm   bool
	}
	cases := []struct {
		row   string
		state string
		held  bool
		in    steeringReconcileInputs
		want  want
	}{
		{"1 absent: stale order entry dropped", "", false, steeringReconcileInputs{recorded: unrecorded}, want{state: "", inOrder: false}},
		{"2 accepted, in flight: left", "accepted", false, steeringReconcileInputs{inFlight: true, recorded: unrecorded}, want{state: "accepted", inOrder: true}},
		{"3 accepted, parked: left parked", "accepted", true, steeringReconcileInputs{recorded: unrecorded}, want{state: "accepted", inOrder: true, held: true}},
		{"3 accepted, Stop: hold re-asserted", "accepted", false, steeringReconcileInputs{recorded: unrecorded, stopping: true}, want{state: "accepted", inOrder: true, held: true}},
		{"4 accepted, runnable: left, no re-arm", "accepted", false, steeringReconcileInputs{recorded: unrecorded}, want{state: "accepted", inOrder: true}},
		{"5 claimed, in flight: left", "claimed", false, steeringReconcileInputs{inFlight: true, recorded: unrecorded}, want{state: "claimed", inOrder: true}},
		{"5 claimed, in flight, recorded: still left", "claimed", false, steeringReconcileInputs{inFlight: true, recorded: recorded}, want{state: "claimed", inOrder: true}},
		{"6 claimed, recorded: finalized", "claimed", false, steeringReconcileInputs{recorded: recorded}, want{state: "", inOrder: false}},
		{"6 claimed, recorded, parked: finalized, hold untouched", "claimed", true, steeringReconcileInputs{recorded: recorded}, want{state: "", inOrder: false, held: true}},
		{"7 claimed, unrecorded, held: returned and parked", "claimed", true, steeringReconcileInputs{recorded: unrecorded}, want{state: "accepted", inOrder: true, held: true}},
		{"7 claimed, unrecorded, Stop: returned and parked", "claimed", false, steeringReconcileInputs{recorded: unrecorded, stopping: true}, want{state: "accepted", inOrder: true, held: true}},
		{"8 claimed, unrecorded, runnable: returned, retry re-armed", "claimed", false, steeringReconcileInputs{recorded: unrecorded}, want{state: "accepted", inOrder: true, rearm: true}},
	}
	for _, tc := range cases {
		t.Run(tc.row, func(t *testing.T) {
			snapshot := build(tc.state, tc.held, "")
			out := reconcileClientSteering(&snapshot, tc.in)
			pending, ok := snapshot.PendingExecutions[id]
			gotState := ""
			if ok {
				gotState = pending.ExecutionState
			}
			inOrder := false
			for _, o := range snapshot.SteeringOrder {
				inOrder = inOrder || o == id
			}
			if gotState != tc.want.state || inOrder != tc.want.inOrder || snapshot.SteeringHeld != tc.want.held || out.rearm != tc.want.rearm {
				t.Fatalf("state=%q inOrder=%v held=%v rearm=%v, want state=%q inOrder=%v held=%v rearm=%v",
					gotState, inOrder, snapshot.SteeringHeld, out.rearm, tc.want.state, tc.want.inOrder, tc.want.held, tc.want.rearm)
			}
			if tc.want.state == "" && tc.state != "" {
				if snapshot.Journal[id].ExecutionState != "incorporated" || snapshot.Journal[id].OperationState != clientMutationOperationTerminal {
					t.Fatalf("finalized steer's journal = %q/%q, want terminal/incorporated", snapshot.Journal[id].OperationState, snapshot.Journal[id].ExecutionState)
				}
			}
			if tc.want.state == "accepted" && tc.state == "claimed" && snapshot.Journal[id].ExecutionState != "accepted" {
				t.Fatalf("returned steer's journal reads %q, want accepted", snapshot.Journal[id].ExecutionState)
			}
		})
	}

	t.Run("slot: a carrier claim with no turn in flight is released", func(t *testing.T) {
		snapshot := build("accepted", false, turn)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{recorded: unrecorded})
		if snapshot.ActiveTurnID != "" || !out.released {
			t.Fatalf("ActiveTurnID=%q released=%v, want the carrier claim released", snapshot.ActiveTurnID, out.released)
		}
	})
	t.Run("slot: kept while a turn is in flight", func(t *testing.T) {
		snapshot := build("claimed", false, turn)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{inFlight: true, recorded: unrecorded})
		if snapshot.ActiveTurnID != turn || out.released {
			t.Fatalf("ActiveTurnID=%q released=%v, want the running carrier's name kept", snapshot.ActiveTurnID, out.released)
		}
	})
	t.Run("slot: the caller's own name is never released", func(t *testing.T) {
		snapshot := build("claimed", false, turn)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{callerTurn: turn, recorded: unrecorded})
		if snapshot.ActiveTurnID != turn || out.released {
			t.Fatalf("ActiveTurnID=%q released=%v, want the calling turn's name kept", snapshot.ActiveTurnID, out.released)
		}
		if snapshot.PendingExecutions[id].ExecutionState != "accepted" || !out.rearm {
			t.Fatalf("the caller's failed steer reads %q rearm=%v, want returned and re-armed (row 8)", snapshot.PendingExecutions[id].ExecutionState, out.rearm)
		}
	})
	t.Run("slot: a user turn's name is not a carrier claim", func(t *testing.T) {
		snapshot := build("accepted", false, "turn_m9")
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{recorded: unrecorded})
		if snapshot.ActiveTurnID != "turn_m9" || out.released {
			t.Fatalf("ActiveTurnID=%q released=%v, want a name no steer reserved left alone", snapshot.ActiveTurnID, out.released)
		}
	})
}
