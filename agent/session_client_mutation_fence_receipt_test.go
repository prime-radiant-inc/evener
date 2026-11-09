package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// Round-7 S7, pinned as a characterization: no late-rejection delivery path.
// Queue and steer acceptance return an applied receipt immediately
// (AcceptClientMutationQueueAs/AcceptClientMutationSteerAs build it inside the
// accept transaction), long before the input is consumed; the hub forwards
// that receipt, and the caller's success is terminal. A typed mismatch the
// fence would discover at consumption reaches no one: the durable record's
// answer to every later retry is the applied response recorded at acceptance
// (applyClientMutationRecord stores the marshaled applied receipt as the
// record's Result; the replay path decodes exactly that), the record's
// Rejection field is written only at acceptance time, and no watch event,
// correlated rejection outcome, or re-read trigger exists for the caller
// already holding success.
//
// Promised contract (red pending machinery): the receipt IS the refusal. With
// the pair comparison inside the admission transaction, a mismatched answer is
// refused synchronously — the acceptance response is the typed refusal — so no
// late-rejection channel is built at all. The seam a real red needs: the pair
// fields on the mutation params and the admission-transaction comparison; see
// specs/manager-tools/fence-spike.md. This characterization pins today's
// terminality so the implementer lane flips it: post-change, a stale pair
// yields a typed refusal receipt instead of an applied one, and nothing about
// an accepted answer can be discovered "later".
func TestClientMutationQueueReceiptIsTerminalAppliedEvidence(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	disarmTestInterruptFence(t, sess)
	params := appwire.TurnQueueParams{
		ClientMutationID: "mutation-late-rejection",
		Input:            []appwire.InputItem{{Type: "text", Text: "delayed answer"}},
	}

	// Acceptance: applied, immediately, while the input has not run.
	first, err := sess.AcceptClientMutationQueue(params)
	if err != nil {
		t.Fatalf("accept queue: %v", err)
	}
	if first.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("first disposition = %q, want applied", first.Receipt.Disposition)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 {
		t.Fatalf("queue depth after acceptance = %d, want 1 (the input has not run)", len(snapshot.InputQueue))
	}
	record := snapshot.Journal[params.ClientMutationID]
	if record.Rejection != nil {
		t.Fatalf("accepted record carries a rejection: %+v", record.Rejection)
	}
	if len(record.Result) == 0 {
		t.Fatal("accepted record carries no durable result — the replay path decodes this byte string")
	}

	// Every later retry replays the applied result: the caller holding success
	// can never learn a consumption-time outcome, because the record answers
	// with the acceptance-time receipt forever.
	replayed, err := sess.AcceptClientMutationQueue(params)
	if err != nil {
		t.Fatalf("replay queue: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("replayed disposition = %q, want replayed", replayed.Receipt.Disposition)
	}
	if replayed.Receipt.TurnID != first.Receipt.TurnID || len(replayed.Receipt.QueueEntryIDs) != len(first.Receipt.QueueEntryIDs) {
		t.Fatalf("replay did not decode the recorded applied result: first=%+v replayed=%+v", first.Receipt, replayed.Receipt)
	}
}
