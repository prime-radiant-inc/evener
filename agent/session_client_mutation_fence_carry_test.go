package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// Round-7 T3, pinned as a characterization: the pair's named carry is the
// wrong record shape. Spec 4 named clientMutationQueueEntry as the queued
// intent's pair carry — the QUEUE-method entry. But a steer's durable carrier
// is the client-mutation JOURNAL record (AcceptClientMutationSteer journals
// through addPendingSteering and the record's own payload), and steers never
// create a queue entry at all: pair fields added to clientMutationQueueEntry
// leave steer records unfenced across crash-restart, and the crash window the
// fence exists for opens anyway.
//
// This characterization pins the shapes through the real accept paths:
// the journal record is the one durable carrier BOTH methods share, it is the
// only carrier a steer has, and it already carries the precondition idiom
// (clientMutationPreconditions.ExpectedQueueRevision, the ExpectedQueueRevision
// admission-check pattern appwire/types_turn.go documents) that a fence pair
// extends — while the queue entry type carries no precondition field at all.
// The seam a real red needs: the pair on clientMutationRecord (the
// Preconditions extension), never on the queue entry; see
// specs/manager-tools/fence-spike.md.
func TestClientMutationSteerCarriesThroughTheJournalRecordNotTheQueueEntry(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	disarmTestInterruptFence(t, sess)

	// A steer is durably journaled — round 7's evidence-corrected fact — and
	// creates NO queue entry.
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "mutation-steer-carry",
		Input:            []appwire.InputItem{{Type: "text", Text: "steered note"}},
	}); err != nil {
		t.Fatalf("accept steer: %v", err)
	}
	snapshot := sess.clientMutations.snapshot()
	steerRecord, ok := snapshot.Journal["mutation-steer-carry"]
	if !ok || steerRecord.Method != clientMutationMethodSteer {
		t.Fatalf("steer journal record = %+v, want a durable steer record", steerRecord)
	}
	if len(steerRecord.Payload) == 0 {
		t.Fatal("steer journal record carries no payload")
	}
	if len(snapshot.InputQueue) != 0 {
		t.Fatalf("steer created queue entries: %+v — the queue entry is not the steer's carrier", snapshot.InputQueue)
	}

	// A queue is journaled too — the record is the shared carrier — and also
	// appends a queue entry, which is what spec 4 named as the carry.
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "mutation-queue-carry",
		Input:            []appwire.InputItem{{Type: "text", Text: "queued input"}},
	}); err != nil {
		t.Fatalf("accept queue: %v", err)
	}
	snapshot = sess.clientMutations.snapshot()
	queueRecord, ok := snapshot.Journal["mutation-queue-carry"]
	if !ok || queueRecord.Method != clientMutationMethodQueue {
		t.Fatalf("queue journal record = %+v, want a durable queue record", queueRecord)
	}
	if len(snapshot.InputQueue) != 1 || snapshot.InputQueue[0].ClientMutationID != "mutation-queue-carry" {
		t.Fatalf("queue entries = %+v, want the accepted queue entry", snapshot.InputQueue)
	}

	// The shape divergence a fence pair must survive: the journal record has
	// the precondition field the pair extends; the queue entry has none, so a
	// pair placed there is invisible to every steer and to the drain path the
	// consumption side walks.
	if _, ok := reflect.TypeFor[clientMutationRecord]().FieldByName("Preconditions"); !ok {
		t.Fatal("clientMutationRecord lost its Preconditions field — the pair's landing zone moved")
	}
	if _, ok := reflect.TypeFor[clientMutationQueueEntry]().FieldByName("Preconditions"); ok {
		t.Fatal("clientMutationQueueEntry gained a Preconditions field — re-evaluate the carry: the steer record must carry the pair too")
	}
	// And the wire shapes agree: the record serializes its preconditions, the
	// queue entry has no field for one.
	rev := uint64(3)
	record, err := json.Marshal(clientMutationRecord{Preconditions: clientMutationPreconditions{ExpectedQueueRevision: &rev}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record), "preconditions") {
		t.Fatalf("journal record does not serialize preconditions: %s", record)
	}
	entry, err := json.Marshal(clientMutationQueueEntry{ID: "queue_1", ClientMutationID: "mutation-queue-carry"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(entry), "preconditions") {
		t.Fatalf("queue entry serializes preconditions: %s", entry)
	}

	// The journal record is also the crash-restart carrier: a fresh store over
	// the same state directory replays the steer from it.
	reopened, err := newClientMutationStore(sess.stateDir, sess.ID())
	if err != nil {
		t.Fatalf("reopen client mutation store: %v", err)
	}
	resnapshot := reopened.snapshot()
	if _, ok := resnapshot.Journal["mutation-steer-carry"]; !ok {
		t.Fatal("reopened store lost the steer journal record — the steer has no other durable carry")
	}
}
