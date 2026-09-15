package agent

import (
	"fmt"
	"slices"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// steeringReconcileInputs are the facts reconcileClientSteering decides on.
// They are sampled by the caller BEFORE the store mutate that runs the table:
// the transcript question takes s.mu, and the store serializer must never wait
// on that lock (see InterruptClientMutation's sample of sessionRunning).
type steeringReconcileInputs struct {
	// inFlight reports that a turn is running in this process. popSteeringHead
	// runs only inside a turn, so a claimed steer under a running turn is one
	// that turn is appending right now, and the table leaves it alone. The
	// wake-time and restore-time callers answer it from the session state and
	// the active-turn slot (turnInFlight); the caller that IS the turn passes
	// false and names itself in callerTurn -- it is handing a steer back, not
	// racing one.
	inFlight bool
	// callerTurn is the id of the turn calling from inside its own run
	// (consumeSteeringMessage's failure path), or empty. The slot rule never
	// releases the caller's own name.
	callerTurn string
	// recorded reports whether the transcript holds the steering turn for a
	// client mutation id. Nil means "nothing is recorded" and is only right
	// for a caller that knows so.
	recorded func(clientMutationID string) bool
	// stopping reports that a Stop is finalizing: every steer the table
	// returns to the queue is parked, and every accepted steer stays parked.
	stopping bool
}

// steeringReconcileOutcome is what the table did, so the caller can act on it
// without deciding anything of its own.
type steeringReconcileOutcome struct {
	// returned lists the steers put back to accepted.
	returned []string
	// finalized lists the steers incorporated (recorded, incorporation write
	// caught up).
	finalized []string
	// dropped lists stale order entries removed.
	dropped []string
	// rearm reports that a steer went back to accepted unparked: nothing
	// else will run it, so the caller re-arms the carrier retry.
	rearm bool
	// released reports that the active-turn slot was released from a carrier
	// claim that never ran.
	released bool
}

func (in steeringReconcileInputs) isRecorded(id string) bool {
	return in.recorded != nil && in.recorded(id)
}

// reconcileClientSteering is the ONE place that decides what happens to a
// client steer no turn is delivering. Every path that used to decide on its
// own -- the interrupt's finalization, the carrier retry's timer, restore, the
// carrier's own failure exit -- calls this and acts on the outcome. The claim
// (claimSteeringCarrierTurn) is the only other durable act on a steer, and a
// claim the store refuses re-arms the same retry.
//
// The table. ExecutionState is the durable state of the steer's pending
// execution; "in flight" is steeringReconcileInputs.inFlight; "recorded" is
// whether the transcript holds the steering turn; "parked" is
// snapshot.SteeringHeld || stopping.
//
//	#  ExecutionState  in flight  recorded  parked  action
//	1  absent          any        any       any     drop the order entry (see below)
//	2  accepted        yes        any       any     leave: the running turn drains it at its next boundary, or it is the running carrier's own claim window
//	3  accepted        no         any       yes     leave parked; a Stop re-asserts SteeringHeld
//	4  accepted        no         any       no      leave: runnable, the wake path owns it
//	5  claimed         yes        any       any     leave: the turn that popped it is appending it
//	6  claimed         no         yes       any     finalize: the append landed, only the incorporation write failed
//	7  claimed         no         no        yes     return to accepted and park: the append never landed, a Stop or hold owns the next run
//	8  claimed         no         no        no      return to accepted and re-arm the retry: the append never landed and nothing else will run it
//
// The active-turn slot: when no turn is in flight and the slot names a steer's
// reserved id, that is a carrier claim that never ran to incorporation
// (claimSteeringCarrierTurn publishes the id before the carrier opens; a
// process death or a cancelled input leaves it), and it is released so the
// next claim and the next turn/start are not refused. The caller's own name is
// never released here.
//
// Row 1: an order entry with no pending execution is a remnant of a
// finalization that retired the steer -- every retiring path nils the journal
// payload, so there is no input left to rebuild a pending execution from. The
// entry would sit at the head of the order and refuse popSteeringHead ("not
// pending") for everything behind it, so it is dropped; the journal keeps the
// outcome it recorded.
//
// Failures: this function never fails on its own; the store write that
// carries its result can. The session-level wrapper re-arms the carrier retry
// on that failure, so the table is re-run from the timer rather than warned
// about and forgotten.
func reconcileClientSteering(snapshot *clientMutationSnapshot, in steeringReconcileInputs) steeringReconcileOutcome {
	var out steeringReconcileOutcome
	parked := snapshot.SteeringHeld || in.stopping
	for _, id := range slices.Clone(snapshot.SteeringOrder) {
		pending, ok := snapshot.PendingExecutions[id]
		if !ok {
			// Row 1.
			removeClientMutationSteeringOrder(snapshot, id)
			out.dropped = append(out.dropped, id)
			continue
		}
		switch pending.ExecutionState {
		case "accepted":
			// Rows 2-4: an accepted steer is where it belongs; a Stop only
			// re-asserts the hold that keeps it there.
			if !in.inFlight && in.stopping {
				snapshot.SteeringHeld = true
			}
		case "claimed":
			if in.inFlight {
				// Row 5.
				continue
			}
			record := snapshot.Journal[id]
			if in.isRecorded(id) {
				// Row 6.
				record.OperationState = clientMutationOperationTerminal
				record.ExecutionState = "incorporated"
				record.ProjectionState = appwire.MutationProjectionReflected
				record.Payload = nil
				snapshot.Journal[id] = record
				delete(snapshot.PendingExecutions, id)
				removeClientMutationSteeringOrder(snapshot, id)
				out.finalized = append(out.finalized, id)
				continue
			}
			// Rows 7 and 8.
			pending.ExecutionState = "accepted"
			snapshot.PendingExecutions[id] = pending
			record.ExecutionState = "accepted"
			snapshot.Journal[id] = record
			out.returned = append(out.returned, id)
			if parked {
				snapshot.SteeringHeld = true
			} else {
				out.rearm = true
			}
		}
	}
	if !in.inFlight && snapshot.ActiveTurnID != "" && snapshot.ActiveTurnID != in.callerTurn && steeringOrderNamesTurn(snapshot, snapshot.ActiveTurnID) {
		snapshot.ActiveTurnID = ""
		out.released = true
	}
	return out
}

// steeringOrderNamesTurn reports whether a pending steer in the order reserved
// turnID -- the id a steering carrier runs under.
func steeringOrderNamesTurn(snapshot *clientMutationSnapshot, turnID string) bool {
	if turnID == "" {
		return false
	}
	for _, id := range snapshot.SteeringOrder {
		if pending, ok := snapshot.PendingExecutions[id]; ok && pending.TurnID == turnID {
			return true
		}
	}
	return false
}

// reconcileClientSteering runs the table against the store and acts on the
// outcome: the queue is re-read from the store, a steer returned unparked
// re-arms the carrier retry (row 8), and a store write that refused the result
// re-arms it too, so the table runs again from the timer. The transcript
// question is sampled here, before the mutate, unless the caller supplied it.
func (s *Session) reconcileClientSteering(in steeringReconcileInputs) steeringReconcileOutcome {
	if s.clientMutations == nil {
		return steeringReconcileOutcome{}
	}
	if in.recorded == nil {
		in.recorded = s.recordedClientSteering()
	}
	var out steeringReconcileOutcome
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		out = reconcileClientSteering(snapshot, in)
		return nil
	})
	s.reflectDurableClientSteering()
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("reconciling client steering failed: %v; the steering retry runs it again", err)})
		s.scheduleSteeringCarrierRetry()
		return steeringReconcileOutcome{}
	}
	if out.rearm {
		s.scheduleSteeringCarrierRetry()
	}
	return out
}

// recordedClientSteering samples, under s.mu, which client mutations the
// transcript (as history holds it) has a steering turn for, and returns the
// question over that sample. Taken before a store mutate, never inside one.
func (s *Session) recordedClientSteering() func(string) bool {
	recorded := map[string]bool{}
	s.mu.Lock()
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && turn.ClientMutationID != "" {
			recorded[turn.ClientMutationID] = true
		}
	}
	s.mu.Unlock()
	return func(id string) bool { return recorded[id] }
}

// turnInFlight answers steeringReconcileInputs.inFlight for a caller outside
// any turn: the session is processing, or the active-turn slot is taken --
// in this process, a taken slot is a running turn or a carrier claim about to
// open one.
func (s *Session) turnInFlight() bool {
	if s.State() == SessionProcessing {
		return true
	}
	return s.clientMutations != nil && s.clientMutations.snapshot().ActiveTurnID != ""
}
