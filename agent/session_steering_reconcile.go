package agent

import "slices"

// reconcileClientSteering is the one place that settles the durable state of
// client steering no turn is delivering: the interrupt's finalization (a Stop,
// stopping) and restore call it. A steer has one durable state, accepted, from
// its acceptance until finalizeIncorporatedSteering records that its transcript
// append landed; the transcript is the only record of delivery, so there is
// nothing here to return or re-arm. What the rules do:
//
//	0  a steer the transcript holds is finalized: its append landed and only
//	   the store's incorporation write did not (a process that died in
//	   between, at restore; a store that refused the write, in this process).
//	   recorded answers from the loaded transcript at restore and from the
//	   in-flight set (recordedSteeringAwaitingMark) at a Stop -- never a
//	   history scan inside the store's serializer.
//	1  an order entry with no pending execution is dropped: a remnant of a
//	   finalization that retired the steer (every retiring path nils the
//	   journal payload, so there is no input to rebuild it from). Left, it
//	   would sit at the head of the order and refuse popSteeringHead for
//	   everything behind it.
//	2  a Stop parks every accepted steer (SteeringHeld), the way a queued
//	   message a Stop returns is parked (wms7): the user's next run carries
//	   it. Restore is not a Stop and parks nothing.
//	H  a hold naming no pending user steer is released. A Stop arms the hold
//	   for whatever was pending at its acceptance; the steer a cancelled
//	   carrier appended in time is gone by finalization, and a hold naming
//	   nothing swallows the next steer the user sends (#710). Release only,
//	   never arm.
//
// The active-turn slot is not this function's: a carrier claim the process
// died under is released at load by forgetRunningTurnNoOneOwns, and in this
// process by the claimant's own deferred release.
func reconcileClientSteering(snapshot *clientMutationSnapshot, recorded func(clientMutationID string) bool, stopping bool) {
	for _, id := range slices.Clone(snapshot.SteeringOrder) {
		if _, ok := snapshot.PendingExecutions[id]; !ok {
			removeClientMutationSteeringOrder(snapshot, id)
		} else if recorded(id) {
			finalizeSteeringInSnapshot(snapshot, id)
		}
	}
	pending := snapshotHasPendingUserSteering(snapshot)
	if stopping && pending {
		snapshot.SteeringHeld = true
	}
	if !pending {
		snapshot.SteeringHeld = false
	}
}
