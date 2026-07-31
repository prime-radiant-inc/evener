package doctor

import (
	"path/filepath"
	"strings"
	"testing"
)

// storeWithBothOutcomes is a client-mutation store holding the two record kinds
// that make the journal decisive: an accepted turn/start and a rejected
// turn/queue, plus a queued input still waiting for the running turn.
const storeWithBothOutcomes = `{
  "version": 1,
  "session_id": "%SID%",
  "active_turn_id": "01TURNSTART",
  "accepted_turns": 1,
  "journal": {
    "cm-start": {
      "client_mutation_id": "cm-start",
      "method": "turn/start",
      "payload": {"input": [{"type": "text", "text": "hello"}]},
      "payload_hash": "9c1185a5c5e9fc54612808977ee8f548b2258d31",
      "stable_turn_id": "01TURNSTART",
      "operation_state": "applied",
      "execution_state": "running",
      "projection_state": "reflected",
      "attempt_generation": 1
    },
    "cm-queued": {
      "client_mutation_id": "cm-queued",
      "method": "turn/queue",
      "payload": {"input": [{"type": "text", "text": "queued reply"}]},
      "payload_hash": "b6589fc6ab0dc82cf12099d1c2d40ab994e8410c",
      "stable_queue_entry_ids": ["q1"],
      "operation_state": "applied",
      "execution_state": "queued",
      "projection_state": "reflected",
      "attempt_generation": 1
    },
    "cm-steer": {
      "client_mutation_id": "cm-steer",
      "method": "turn/steer",
      "payload_hash": "356a192b7913b04c54574d18c28d46e6395428ab",
      "operation_state": "rejected",
      "execution_state": "rejected",
      "projection_state": "removed",
      "attempt_generation": 1,
      "rejection": {
        "code": -32013,
        "message": "turn is not running",
        "data": {
          "serf_error_info": "conflict",
          "client_mutation_id": "cm-steer",
          "mutation_outcome": "notAccepted",
          "retry_disposition": "none"
        }
      }
    }
  },
  "input_queue": [
    {"id": "q1", "client_mutation_id": "cm-queued", "input": [{"type": "text", "text": "queued reply"}]}
  ],
  "queue_revision": 3,
  "next_turn_sequence": 2,
  "next_queue_entry_sequence": 2,
  "budget_reservations": {"cm-start": {"turn_id": "01TURNSTART", "slots": 1}},
  "pending_executions": {
    "cm-start": {
      "client_mutation_id": "cm-start",
      "method": "turn/start",
      "input": [{"type": "text", "text": "hello"}],
      "execution_state": "running",
      "turn_id": "01TURNSTART",
      "projection_state": "pending"
    }
  },
  "steering_order": ["cm-steer"]
}`

// writeMutationStore lays the client-mutation store down beside a session's
// sessions/ dir, where the runtime writes it.
func writeMutationStore(t *testing.T, bucketDir, sid, body string) {
	t.Helper()
	writeFile(t, filepath.Join(bucketDir, "mutations", sid+".json"), strings.ReplaceAll(body, "%SID%", sid))
}

// emptyStore is the shape the runtime writes before anything is journaled: the
// three containers are present and empty. It must not read as "no store".
const emptyStore = `{
  "version": 1,
  "session_id": "%SID%",
  "accepted_turns": 0,
  "journal": {},
  "input_queue": null,
  "queue_revision": 0,
  "next_turn_sequence": 0,
  "next_queue_entry_sequence": 0,
  "budget_reservations": {},
  "pending_executions": {}
}`

func TestMutations_EmptyStoreIsPresentWithNoRecords(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	writeMutationStore(t, bucket, sidA, emptyStore)

	got, err := Mutations(base, sidA)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	// An empty store and a missing store are different answers: one says the
	// daemon wrote a store and journaled nothing, the other that it never wrote
	// one at all.
	if !got.Present {
		t.Error("Present = false for a store that exists on disk with an empty journal")
	}
	if len(got.Journal) != 0 || len(got.InputQueue) != 0 || len(got.PendingExecutions) != 0 {
		t.Errorf("empty store reported rows: %+v", got)
	}
}

// A session that accepted no client mutations has no store file at all. That is
// the answer "nothing reached the daemon", so it must report cleanly rather than
// failing the way an unreadable artifact does.
func TestMutations_MissingStoreIsCleanNotAnError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)

	got, err := Mutations(base, sidA)
	if err != nil {
		t.Fatalf("Mutations on a session with no store: %v", err)
	}
	if got.Present {
		t.Error("Present = true with no store on disk")
	}
	if len(got.Journal) != 0 || len(got.InputQueue) != 0 || len(got.PendingExecutions) != 0 {
		t.Errorf("missing store reported rows: %+v", got)
	}
	// The path is still reported: it is where the store WOULD be.
	if got.MutationsPath != filepath.Join(bucket, "mutations", sidA+".json") {
		t.Errorf("MutationsPath = %q, want the path the store would occupy", got.MutationsPath)
	}
}

// Bytes that are not a client-mutation snapshot must fail loudly and name the
// file, so the reader is sent to the artifact instead of drawing a conclusion
// from a report the decode could not fill in.
func TestMutations_MalformedStoreNamesTheFile(t *testing.T) {
	for name, body := range map[string]string{
		"truncated":  `{"version":1,"session_id":"%SID%","journal":{`,
		"zero bytes": ``,
		"not json":   `<html>gateway timeout</html>`,
		"trailing":   `{"version":1,"session_id":"%SID%","journal":{},"budget_reservations":{},"pending_executions":{}}{"version":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			bucket := stateHomeBucket(base, hash1)
			writeSession(t, bucket, sidA)
			writeMutationStore(t, bucket, sidA, body)

			_, err := Mutations(base, sidA)
			if err == nil {
				t.Fatal("malformed store decoded without error")
			}
			path := filepath.Join(bucket, "mutations", sidA+".json")
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

// The reader mirrors a runtime type it cannot import, so drift and wrong files
// both have to be loud. An empty journal is the strongest claim this tool makes
// ("the request never reached the daemon") and it must never be produced by a
// file the reader failed to understand.
func TestMutations_WrongShapeNeverRendersAsAnEmptyJournal(t *testing.T) {
	for name, body := range map[string]string{
		// A field the mirror does not know: the runtime snapshot grew and this
		// reader would otherwise drop it silently from every diagnosis.
		"unknown field": `{"version":1,"session_id":"%SID%","journal":{},"budget_reservations":{},
		    "pending_executions":{},"delivery_receipts":{"cm-1":"sent"}}`,
		// Containers the runtime always writes: absent means these are not the
		// store's bytes.
		"absent journal":             `{"version":1,"session_id":"%SID%","budget_reservations":{},"pending_executions":{}}`,
		"absent pending executions":  `{"version":1,"session_id":"%SID%","journal":{},"budget_reservations":{}}`,
		"absent budget reservations": `{"version":1,"session_id":"%SID%","journal":{},"pending_executions":{}}`,
		"empty object":               `{}`,
		"json null":                  `null`,
		"future version":             `{"version":2,"session_id":"%SID%","journal":{},"budget_reservations":{},"pending_executions":{}}`,
		"another session's store":    `{"version":1,"session_id":"02wMz5TxvEMoJEDTDGOTim","journal":{},"budget_reservations":{},"pending_executions":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			bucket := stateHomeBucket(base, hash1)
			writeSession(t, bucket, sidA)
			writeMutationStore(t, bucket, sidA, body)

			got, err := Mutations(base, sidA)
			if err == nil {
				t.Fatalf("wrong-shape store reported as a %d-record journal instead of erroring: %+v", len(got.Journal), got)
			}
			if !strings.Contains(err.Error(), filepath.Join(bucket, "mutations", sidA+".json")) {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

func TestMutations_AcceptedAndRejectedRecords(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	writeMutationStore(t, bucket, sidA, storeWithBothOutcomes)

	got, err := Mutations(base, sidA)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if !got.Present {
		t.Fatal("Present = false, want true for a session with a store on disk")
	}
	if got.AcceptedTurns != 1 || got.QueueRevision != 3 {
		t.Errorf("accepted_turns/queue_revision = %d/%d, want 1/3", got.AcceptedTurns, got.QueueRevision)
	}
	if len(got.Journal) != 3 {
		t.Fatalf("journal = %d records, want 3: %+v", len(got.Journal), got.Journal)
	}
	// Sorted by client mutation ID: the map on disk has no order of its own.
	wantIDs := []string{"cm-queued", "cm-start", "cm-steer"}
	for i, want := range wantIDs {
		if got.Journal[i].ClientMutationID != want {
			t.Errorf("journal[%d] = %q, want %q", i, got.Journal[i].ClientMutationID, want)
		}
	}
	accepted := got.Journal[1]
	if accepted.Method != "turn/start" || accepted.OperationState != "applied" ||
		accepted.ExecutionState != "running" || accepted.StableTurnID != "01TURNSTART" {
		t.Errorf("accepted record = %+v, want turn/start applied/running turn 01TURNSTART", accepted)
	}
	rejected := got.Journal[2]
	if rejected.OperationState != "rejected" || rejected.ExecutionState != "rejected" {
		t.Errorf("rejected record states = %q/%q, want rejected/rejected", rejected.OperationState, rejected.ExecutionState)
	}
	if rejected.RejectionCode != -32013 || rejected.RejectionMessage != "turn is not running" {
		t.Errorf("rejection = %d %q, want -32013 %q", rejected.RejectionCode, rejected.RejectionMessage, "turn is not running")
	}
	if len(got.InputQueue) != 1 || got.InputQueue[0].EntryID != "q1" ||
		got.InputQueue[0].ClientMutationID != "cm-queued" || got.InputQueue[0].Items != 1 {
		t.Errorf("input_queue = %+v, want one q1/cm-queued entry with 1 item", got.InputQueue)
	}
	if len(got.PendingExecutions) != 1 || got.PendingExecutions[0].ClientMutationID != "cm-start" ||
		got.PendingExecutions[0].ExecutionState != "running" || got.PendingExecutions[0].TurnID != "01TURNSTART" {
		t.Errorf("pending_executions = %+v, want one running cm-start on turn 01TURNSTART", got.PendingExecutions)
	}
	if got.MutationsPath != filepath.Join(bucket, "mutations", sidA+".json") {
		t.Errorf("MutationsPath = %q", got.MutationsPath)
	}
}

func TestMutations_RenderCarriesTheDecisiveFields(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	writeMutationStore(t, bucket, sidA, storeWithBothOutcomes)

	report, err := Mutations(base, sidA)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	out := RenderMutations(report)
	for _, want := range []string{
		"accepted turns: 1  queue revision: 3",
		"journal: 3 mutations (1 rejected)",
		"cm-start  method=turn/start  operation=applied  execution=running  turn=01TURNSTART",
		`cm-steer  method=turn/steer  operation=rejected  execution=rejected  rejection=-32013 "turn is not running"`,
		"input queue: 1 entry",
		"q1  mutation=cm-queued  items=1",
		"pending executions: 1",
		"cm-start  method=turn/start  execution=running  turn=01TURNSTART  projection=pending",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

// The missing-store render has to say so in words: a bare "journal: 0 mutations"
// would read as "the daemon has a store and nothing is in it".
func TestMutations_RenderSaysWhenThereIsNoStore(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)

	report, err := Mutations(base, sidA)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	out := RenderMutations(report)
	if !strings.Contains(out, "no client-mutation store on disk") {
		t.Errorf("render does not say the store is absent:\n%s", out)
	}
	if strings.Contains(out, "journal:") {
		t.Errorf("render reports a journal for a session with no store:\n%s", out)
	}
}
