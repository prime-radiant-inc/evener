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
	// holdReleased reports that a steering hold naming no pending user steer
	// was released.
	holdReleased bool
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
// Two rules follow the rows, over the whole store rather than one steer:
//
//	S  the active-turn slot: when no turn is in flight and the slot names a
//	   steer's reserved id, that is a carrier claim that never ran to
//	   incorporation (claimSteeringCarrierTurn publishes the id before the
//	   carrier opens; a process death or a cancelled input leaves it), and it
//	   is released so the next claim and the next turn/start are not refused.
//	   Whether the slot names a steer is read BEFORE the rows run: row 6 (or
//	   row 1) removes the steer from the order, and a carrier that recorded
//	   its steer and died before the incorporation write is exactly the claim
//	   this rule exists to release. The caller's own name is never released.
//	H  the hold: after the rows, a SteeringHeld that names no pending user
//	   steer is released. A Stop arms the hold for whatever was pending at
//	   its acceptance, and rows 1 and 6 can retire the last of that; a hold
//	   naming nothing swallows the next steer the user sends (#710). This is
//	   a release only, never an arm: restore is not a Stop.
//
// Two more rules are the callers', not the rows', and are listed here so the
// whole contract reads in one place:
//
//	B  the input boundary: every input runs the table with no turn in flight
//	   as it settles (processInputKindWithProvenance's idle tail). Row 5 leaves
//	   a claimed steer alone while ANY turn runs -- a passenger mid-append and
//	   a stale claim look the same from the store, both claimed under an id
//	   that is not the running turn's -- so the turn that just ended is the
//	   moment a stale claim (a carrier whose append and claim return both
//	   failed) comes back through row 8. Nothing else runs the table after a
//	   turn the steer did not belong to.
//	L  the per-input latch: a steering append that fails inside an input
//	   (consumeSteeringMessage) latches steeringDrainRefused for that input;
//	   every later injection point of the same input -- the next tool round,
//	   the drain ladder's carrier claim -- stands down, so one input spends
//	   one attempt and the carrier retry owns the next. Cleared when the
//	   next input starts. A refused pop claim (popSteeringHead) and a refused
//	   incorporation write (row 6 re-run from consumeSteeringMessage) re-arm
//	   the same retry; the retry budget clears only once incorporation lands.
//
// A stale slot: a release write the store refused leaves the slot named until
// the release retry lands (scheduleRunningTurnReleaseRetry). Until then a
// caller outside any turn reads the slot as in flight (turnInFlight) and rows
// 2 and 5 leave the steer; the release retry, when it lands, runs this table
// again and wakes, which is what delivers the steer the stale slot held back.
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
	// Rule S's fact, taken before the rows can remove the steer that names it.
	carrierClaim := !in.inFlight && snapshot.ActiveTurnID != "" && snapshot.ActiveTurnID != in.callerTurn &&
		steeringOrderNamesTurn(snapshot, snapshot.ActiveTurnID)
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
	// Rule S.
	if carrierClaim {
		snapshot.ActiveTurnID = ""
		out.released = true
	}
	// Rule H.
	if snapshot.SteeringHeld && !snapshotHasPendingUserSteering(snapshot, "") {
		snapshot.SteeringHeld = false
		out.holdReleased = true
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

// latchSteeringDrainRefused records, for the input being processed, that a
// steering append failed (rule L).
func (s *Session) latchSteeringDrainRefused() {
	s.steeringRetryMu.Lock()
	s.steeringDrainRefused = true
	s.steeringRetryMu.Unlock()
}

// steeringDrainRefusedThisInput reports rule L's latch.
func (s *Session) steeringDrainRefusedThisInput() bool {
	s.steeringRetryMu.Lock()
	defer s.steeringRetryMu.Unlock()
	return s.steeringDrainRefused
}

// clearSteeringDrainRefused opens the next input's first attempt (rule L).
func (s *Session) clearSteeringDrainRefused() {
	s.steeringRetryMu.Lock()
	s.steeringDrainRefused = false
	s.steeringRetryMu.Unlock()
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
// in this process, a taken slot is a running turn, a carrier claim about to
// open one, or a stale name a refused release write left behind. The last
// reads as in flight too, deliberately: the release retry that clears it runs
// the table again and wakes (scheduleRunningTurnReleaseRetry), so nothing is
// lost by leaving the steer until then, and nothing is written into a slot
// the retry is about to clear.
func (s *Session) turnInFlight() bool {
	if s.State() == SessionProcessing {
		return true
	}
	return s.clientMutations != nil && s.clientMutations.snapshot().ActiveTurnID != ""
}
