package hostops

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// interrupts records by id for the boot-pass assertions.
func recordsByID(records []Record) map[string]Record {
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	return byID
}

// seedBootStore builds a store holding one pending, one running, one complete
// and one failed record, in that stored order, and returns the store file's
// path, the four ids in that same order, and the records exactly as the store
// holds them once seeding is done — the state the boot pass starts from.
func seedBootStore(t *testing.T) (*Store, string, [4]string, map[string]Record) {
	t.Helper()
	store, path := openTestStore(t)
	pending := createTestRecord(t, store, "h1")
	running := createTestRecord(t, store, "h2")
	if _, err := store.Transition(running.ID, StateRunning, func(r *Record) {
		// The worker's fencing epoch is persisted before the first running
		// probe (spec §4); its shape belongs to the crash-fencing spec, so the
		// store carries it verbatim.
		r.FencingEpoch = json.RawMessage(`{"bootId":"boot-1","opSeq":3}`)
	}); err != nil {
		t.Fatalf("Transition(running): %v", err)
	}
	done := createTestRecord(t, store, "h3")
	if _, err := store.Transition(done.ID, StateComplete, nil); err != nil {
		t.Fatalf("Transition(complete): %v", err)
	}
	failed := createTestRecord(t, store, "h4")
	if _, err := store.Transition(failed.ID, StateFailed, nil); err != nil {
		t.Fatalf("Transition(failed): %v", err)
	}
	return store, path, [4]string{pending.ID, running.ID, done.ID, failed.ID}, recordsByID(store.Records())
}

// TestBootPassMovesPendingAndRunningToInterrupted pins spec §7's interrupted
// transition: every record still pending/running at boot becomes interrupted
// with a note naming the crash, each stamped with the value the durable
// sequence advanced to for it; records already in another state are untouched.
func TestBootPassMovesPendingAndRunningToInterrupted(t *testing.T) {
	store, path, ids, seeded := seedBootStore(t)
	pendingID, runningID, doneID, failedID := ids[0], ids[1], ids[2], ids[3]
	before := store.Sequence()

	moved, err := store.RecoverInterrupted()
	if err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}
	if moved != 2 {
		t.Fatalf("boot transitioned %d records, want the 2 pending/running ones", moved)
	}
	if got := store.Sequence(); got != before+2 {
		t.Fatalf("sequence = %d after the boot pass, want %d: one advance per transitioned record", got, before+2)
	}

	byID := recordsByID(store.Records())
	for _, id := range []string{pendingID, runningID} {
		record := byID[id]
		if record.State != StateInterrupted {
			t.Fatalf("record %q is %q after the boot pass, want %q", id, record.State, StateInterrupted)
		}
		if record.Result == nil || record.Result.OK {
			t.Fatalf("record %q carries result %+v, want a failed terminal result", id, record.Result)
		}
		if !strings.Contains(strings.ToLower(record.Result.Message), "crash") {
			t.Fatalf("record %q carries note %q, want a note naming the crash", id, record.Result.Message)
		}
	}
	// The pass stamps in stored order: the pending record is stamped with the
	// first advanced value and the running record with the next.
	if got := byID[pendingID].Sequence; got != before+1 {
		t.Fatalf("the pending record was stamped %d, want %d", got, before+1)
	}
	if got := byID[runningID].Sequence; got != before+2 {
		t.Fatalf("the running record was stamped %d, want %d", got, before+2)
	}
	if byID[doneID].State != StateComplete || byID[doneID].Sequence != seeded[doneID].Sequence {
		t.Fatalf("the complete record changed across the boot pass: %+v", byID[doneID])
	}
	if byID[failedID].State != StateFailed || byID[failedID].Sequence != seeded[failedID].Sequence {
		t.Fatalf("the failed record changed across the boot pass: %+v", byID[failedID])
	}
	if got := byID[runningID].FencingEpoch; string(got) != `{"bootId":"boot-1","opSeq":3}` {
		t.Fatalf("the running record's fencing epoch = %s, want it carried verbatim", got)
	}

	reopened := reopenFresh(t, path)
	if got := reopened.Sequence(); got != before+2 {
		t.Fatalf("reloaded sequence = %d, want the persisted %d", got, before+2)
	}
	reloaded := recordsByID(reopened.Records())
	if reloaded[pendingID].State != StateInterrupted || reloaded[pendingID].Result == nil {
		t.Fatalf("the interrupted transition did not survive the reload: %+v", reloaded[pendingID])
	}
	if reloaded[pendingID].Result.Message != byID[pendingID].Result.Message {
		t.Fatalf("the boot note did not survive the reload")
	}
}

// TestBootPassDoesNotDoubleRun pins that the pass is one-shot: with no
// pending/running record left it moves nothing, advances nothing and does not
// even rewrite the file.
func TestBootPassDoesNotDoubleRun(t *testing.T) {
	store, path, ids, _ := seedBootStore(t)
	if moved, err := store.RecoverInterrupted(); err != nil || moved != 2 {
		t.Fatalf("first RecoverInterrupted: moved=%d err=%v, want 2 and no error", moved, err)
	}
	firstPass := mustReadFile(t, path)
	sequence := store.Sequence()

	moved, err := store.RecoverInterrupted()
	if err != nil {
		t.Fatalf("second RecoverInterrupted: %v", err)
	}
	if moved != 0 {
		t.Fatalf("the second boot pass transitioned %d records, want none", moved)
	}
	if got := store.Sequence(); got != sequence {
		t.Fatalf("the second boot pass advanced the sequence from %d to %d", sequence, got)
	}
	if got := store.Records(); len(got) != 4 {
		t.Fatalf("the second boot pass left %d records, want 4", len(got))
	}
	byID := recordsByID(store.Records())
	if got := byID[ids[0]].Sequence; got != sequence-1 {
		t.Fatalf("the second boot pass re-stamped the first interrupted record with %d, want %d", got, sequence-1)
	}
	if secondPass := mustReadFile(t, path); !bytes.Equal(firstPass, secondPass) {
		t.Fatalf("the second boot pass rewrote the store file:\nfirst:  %s\nsecond: %s", firstPass, secondPass)
	}
}

// TestBootPassLeavesOrphanUnverifiedAlone pins spec §7's one exception:
// orphan-unverified is resolved only through the fencing paths, so the boot
// pass neither transitions it nor advances the sequence for it.
func TestBootPassLeavesOrphanUnverifiedAlone(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	orphan, err := store.Transition(record.ID, StateOrphanUnverified, func(r *Record) {
		r.OrphanBoundary = json.RawMessage(`[{"host":"h1","kind":"local-linux"}]`)
	})
	if err != nil {
		t.Fatalf("Transition(orphan-unverified): %v", err)
	}
	if orphan.Sequence != 0 {
		t.Fatalf("the orphan-unverified record was stamped %d, want 0 (not terminal until resolved)", orphan.Sequence)
	}

	moved, err := store.RecoverInterrupted()
	if err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}
	if moved != 0 {
		t.Fatalf("the boot pass moved %d orphan-unverified records, want none", moved)
	}
	stored, ok := store.Record(record.ID)
	if !ok {
		t.Fatalf("record %q disappeared", record.ID)
	}
	if stored.State != StateOrphanUnverified {
		t.Fatalf("the boot pass moved the orphan-unverified record to %q", stored.State)
	}
	if string(stored.OrphanBoundary) != `[{"host":"h1","kind":"local-linux"}]` {
		t.Fatalf("the persisted boundary = %s, want it carried verbatim", stored.OrphanBoundary)
	}
	if got := store.Sequence(); got != 0 {
		t.Fatalf("sequence = %d, want 0", got)
	}
	reopenFresh(t, path)
}
