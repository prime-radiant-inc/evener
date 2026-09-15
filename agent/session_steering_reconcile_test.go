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
		{"5 claimed, in flight under no active id, no claimant known: left", "claimed", false, steeringReconcileInputs{inFlight: true, recorded: unrecorded}, want{state: "claimed", inOrder: true}},
		{"5 claimed, in flight under no active id, recorded: still left", "claimed", false, steeringReconcileInputs{inFlight: true, recorded: recorded}, want{state: "claimed", inOrder: true}},
		{"6 claimed, recorded: finalized", "claimed", false, steeringReconcileInputs{recorded: recorded}, want{state: "", inOrder: false}},
		{"6 claimed, recorded, parked, last steer: finalized, hold released (rule H)", "claimed", true, steeringReconcileInputs{recorded: recorded}, want{state: "", inOrder: false, held: false}},
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

	// Rows 5 and 5': a turn is in flight, and the claimant says whether it is
	// the one that popped the steer.
	const running = "turn_running"
	ownClaim := func(string) string { return running }
	staleClaim := func(string) string { return "turn_gone" }
	t.Run("5 claimed, in flight, the running turn's own claim: left", func(t *testing.T) {
		snapshot := build("claimed", false, running)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{inFlight: true, recorded: unrecorded, claimant: ownClaim})
		if snapshot.PendingExecutions[id].ExecutionState != "claimed" || out.rearm || len(out.returned) != 0 {
			t.Fatalf("state=%q rearm=%v returned=%v, want the running turn's own claim left alone", snapshot.PendingExecutions[id].ExecutionState, out.rearm, out.returned)
		}
	})
	t.Run("5 claimed, in flight, own claim, recorded: still left", func(t *testing.T) {
		snapshot := build("claimed", false, running)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{inFlight: true, recorded: recorded, claimant: ownClaim})
		if _, still := snapshot.PendingExecutions[id]; !still || len(out.finalized) != 0 {
			t.Fatalf("still=%v finalized=%v, want the running turn's own recorded claim left for its incorporation write", still, out.finalized)
		}
	})
	t.Run("5' claimed, in flight, another turn's claim, unrecorded: returned, re-armed (row 8)", func(t *testing.T) {
		snapshot := build("claimed", false, running)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{inFlight: true, recorded: unrecorded, claimant: staleClaim})
		if snapshot.PendingExecutions[id].ExecutionState != "accepted" || !out.rearm {
			t.Fatalf("state=%q rearm=%v, want a stale claim returned and re-armed", snapshot.PendingExecutions[id].ExecutionState, out.rearm)
		}
		if snapshot.ActiveTurnID != running {
			t.Fatalf("ActiveTurnID=%q, want the running turn's name kept", snapshot.ActiveTurnID)
		}
	})
	t.Run("5' claimed, in flight, another turn's claim, recorded: finalized (row 6)", func(t *testing.T) {
		snapshot := build("claimed", false, running)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{inFlight: true, recorded: recorded, claimant: staleClaim})
		if _, still := snapshot.PendingExecutions[id]; still || len(out.finalized) != 1 {
			t.Fatalf("still=%v finalized=%v, want a recorded stale claim finalized", still, out.finalized)
		}
	})
	t.Run("5' claimed, in flight, another turn's claim, held: returned and parked (row 7)", func(t *testing.T) {
		snapshot := build("claimed", true, running)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{inFlight: true, recorded: unrecorded, claimant: staleClaim})
		if snapshot.PendingExecutions[id].ExecutionState != "accepted" || out.rearm || !snapshot.SteeringHeld {
			t.Fatalf("state=%q rearm=%v held=%v, want a stale claim returned and parked", snapshot.PendingExecutions[id].ExecutionState, out.rearm, snapshot.SteeringHeld)
		}
	})
	t.Run("5' claimed, in flight, no claimant known (a claim from before a restart): returned", func(t *testing.T) {
		snapshot := build("claimed", false, running)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{inFlight: true, recorded: unrecorded})
		if snapshot.PendingExecutions[id].ExecutionState != "accepted" || !out.rearm {
			t.Fatalf("state=%q rearm=%v, want a claim no turn of this process made returned and re-armed", snapshot.PendingExecutions[id].ExecutionState, out.rearm)
		}
	})

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
	t.Run("slot: released even when row 6 finalizes the steer that named it", func(t *testing.T) {
		snapshot := build("claimed", false, turn)
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{recorded: recorded})
		if _, still := snapshot.PendingExecutions[id]; still || len(out.finalized) != 1 {
			t.Fatalf("recorded steer finalized=%v still pending=%v, want finalized", out.finalized, still)
		}
		if snapshot.ActiveTurnID != "" || !out.released {
			t.Fatalf("ActiveTurnID=%q released=%v after finalizing the carrier's own steer, want the claim released", snapshot.ActiveTurnID, out.released)
		}
	})
	t.Run("hold: released once row 6 retires the last steer", func(t *testing.T) {
		snapshot := build("claimed", true, "")
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{recorded: recorded, stopping: true})
		if snapshot.SteeringHeld || !out.holdReleased {
			t.Fatalf("held=%v holdReleased=%v after the last steer was finalized, want the hold released (#710)", snapshot.SteeringHeld, out.holdReleased)
		}
	})
	t.Run("hold: released once row 1 drops the last stale entry", func(t *testing.T) {
		snapshot := build("", true, "")
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{recorded: unrecorded})
		if snapshot.SteeringHeld || !out.holdReleased {
			t.Fatalf("held=%v holdReleased=%v after the stale entry was dropped, want the hold released", snapshot.SteeringHeld, out.holdReleased)
		}
	})
	t.Run("hold: kept while a pending steer names it", func(t *testing.T) {
		snapshot := build("claimed", true, "")
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{recorded: unrecorded})
		if !snapshot.SteeringHeld || out.holdReleased {
			t.Fatalf("held=%v holdReleased=%v with the returned steer parked behind it, want the hold kept (row 7)", snapshot.SteeringHeld, out.holdReleased)
		}
	})
	t.Run("hold: an accepted steer keeps it", func(t *testing.T) {
		snapshot := build("accepted", true, "")
		out := reconcileClientSteering(&snapshot, steeringReconcileInputs{recorded: unrecorded})
		if !snapshot.SteeringHeld || out.holdReleased {
			t.Fatalf("held=%v holdReleased=%v with an accepted steer parked, want the hold kept (row 3)", snapshot.SteeringHeld, out.holdReleased)
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
