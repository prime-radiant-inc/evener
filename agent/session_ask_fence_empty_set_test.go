package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// Round-7 T2, pinned as a characterization: the empty-set branch is undefined.
// A fenced arrival against an ALREADY-CLEARED pending set (the interrupt race:
// the answer was composed against a live set, the set cleared, the arrival
// lands late) has no contract in spec 4 — processOneInput clears
// unconditionally after the durable user turn and the resolving steer checks
// steeringAnswersAsk only; neither conditions on the set being empty. The
// spike's redesign defines the branch as a typed refusal: a fenced arrival
// whose pair names a set that no longer exists is refused like any other
// mismatch, never silently consumed as a plain reply.
//
// Promised contract (red pending machinery): the refusal cannot be expressed
// today — the pair parameters do not exist on TurnQueueParams/TurnSteerParams,
// so no arrival can be "fenced". This characterization pins today's violating
// behavior at the closest observable level: against an empty pending set, a
// queue and a steer are both admitted as ordinary input, applied and with no
// set-emptiness branch anywhere in the admission path. The seam a real red
// needs: the pair fields on the mutation params plus the admission-transaction
// comparison treating an empty set as a mismatch (see
// specs/manager-tools/fence-spike.md).
func TestAskFenceEmptySetArrivalAdmitsUnbranched(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	disarmTestInterruptFence(t, sess)

	if sess.HasPendingAsk() {
		t.Fatal("test setup broken: session must start with an empty pending set")
	}

	// A queued answer-shaped arrival against the empty set: admitted, applied.
	queue, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "mutation-empty-set-queue",
		Input:            []appwire.InputItem{{Type: "text", Text: "delayed answer"}},
	})
	if err != nil {
		t.Fatalf("queue against empty pending set: %v", err)
	}
	if queue.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("queue disposition = %q, want applied (today's unbranched admission)", queue.Receipt.Disposition)
	}

	// A user steer against the empty set: admitted, applied.
	steer, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "mutation-empty-set-steer",
		Input:            []appwire.InputItem{{Type: "text", Text: "delayed answer"}},
	})
	if err != nil {
		t.Fatalf("steer against empty pending set: %v", err)
	}
	if steer.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("steer disposition = %q, want applied (today's unbranched admission)", steer.Receipt.Disposition)
	}

	// The set stays empty — the arrivals were consumed as plain input, and
	// nothing in the admission path observed the emptiness.
	if got := sess.PendingAskCalls(); got == nil || len(got.Calls) != 0 {
		t.Fatalf("pending set after empty-set arrivals = %+v, want authoritative absence", got)
	}
}
