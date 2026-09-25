package transcript

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/fuzz/fault"
	"primeradiant.com/evener/llm"
)

// AppendBatch writes every turn in one write and one fsync, reports the seq the
// first turn took, and leaves the seqs contiguous — the shape A2's fold depends
// on. (It has no production caller until then.)
func TestAppendBatchWritesEveryTurnAtContiguousSeqs(t *testing.T) {
	fs := afero.NewMemMapFs()
	w, err := NewWriterWithFS(fs, "/batch.jsonl", Header{SessionID: "sess-batch"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("one")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("two")),
		schema.NewTurn(schema.TurnUserInput, llm.User("three")),
	}
	firstSeq, err := w.AppendBatch(turns)
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if firstSeq != 0 {
		t.Fatalf("firstSeq = %d, want 0", firstSeq)
	}
	entries := decodeMemEntries(t, fs, "/batch.jsonl")
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	for i, e := range entries {
		if e.Seq != firstSeq+i {
			t.Fatalf("entry %d seq = %d, want %d: batch seqs must be contiguous", i, e.Seq, firstSeq+i)
		}
	}
	next, err := w.AppendBatch([]schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("four"))})
	if err != nil {
		t.Fatalf("AppendBatch (second): %v", err)
	}
	if next != len(turns) {
		t.Fatalf("second batch firstSeq = %d, want %d: the first batch spent %d seqs", next, len(turns), len(turns))
	}
}

// A batch whose write fails and rolls back cleanly is all-or-nothing: nothing
// is in the file and no entry ordinal is spent. Its sequence numbers ARE
// spent: a reader in another process may have seen the lines before the
// rollback took them out, so no later record may reuse them. A retry lands
// the whole batch at the next ordinals and fresh sequence numbers.
// Batch ops after the header (indices 0-3): Seek 4, Write 5 (faulted).
func TestAppendBatchRollsBackWholeBatchOnWriteFailure(t *testing.T) {
	base := afero.NewMemMapFs()
	w, err := newWriterFS(fault.FS(base, fault.FromBytes(faultPlan(5))), faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("alpha")),
		schema.NewTurn(schema.TurnUserInput, llm.User("bravo")),
	}
	if _, err := w.AppendBatch(turns); err == nil {
		t.Fatal("AppendBatch reported success; the injected write failure never reached it")
	}
	if entries := faultTestEntries(t, base); len(entries) != 0 {
		t.Fatalf("entries after a rolled-back batch = %d, want 0: all-or-nothing", len(entries))
	}
	firstSeq, err := w.AppendBatch(turns)
	if err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	if firstSeq != 2 {
		t.Fatalf("retry firstSeq = %d, want 2: the rolled-back batch consumed seq 0 and 1", firstSeq)
	}
	entries := faultTestEntries(t, base)
	if len(entries) != 2 || entries[0].Seq != 2 || entries[1].Seq != 3 {
		t.Fatalf("entries after the retry = %+v, want two at seq 2 and 3", entries)
	}
	if next, err := w.Record(schema.NewTurn(schema.TurnUserInput, llm.User("charlie")), RecordOptions{Door: DoorDurable}); err != nil || next.Ordinal != 2 || next.Seq != 4 {
		t.Fatalf("next record = %+v, %v; want ordinal 2 (the rolled-back batch took none)", next, err)
	}
}

// AppendSynced records AND establishes durability. When the append's own sync
// fails and its rollback cannot take the whole line back out — a retained
// record — the entry is in the file but not durable; AppendSynced's barrier
// then fsyncs the whole file and, if that succeeds, the record is durable and
// AppendSynced returns nil. Durable ops: Seek 4, Write 5, Sync 6 (faulted),
// rollback Truncate 7 (faulted), Seek 8; barrier Sync 9 succeeds.
func TestAppendSyncedReturnsNilWhenTheBarrierConfirmsARetainedRecord(t *testing.T) {
	plan := faultPlan(6)
	plan[7] = 0x00 // rollback Truncate also fails, so the line stays: a retained record
	base := afero.NewMemMapFs()
	w, err := newWriterFS(fault.FS(base, fault.FromBytes(plan)), faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	if err := w.AppendSynced(schema.NewTurn(schema.TurnAssistant, llm.Assistant("recorded and then synced"))); err != nil {
		t.Fatalf("AppendSynced error = %v, want nil: the barrier made the retained record durable", err)
	}
	if w.Poisoned() {
		t.Fatal("a retained-then-synced record poisoned the writer; retained is debt, not poison")
	}
	if warnings := w.DrainWarnings(); len(warnings) != 0 {
		t.Fatalf("AppendSynced left %d warnings; it reports through its return, not the warning channel", len(warnings))
	}
	if entries := faultTestEntries(t, base); len(entries) != 1 {
		t.Fatalf("entries = %d, want the one durable record", len(entries))
	}
}

// AppendSynced returns an error when durability cannot be established, even
// though the line is a record in the file. The writer is NOT poisoned — a whole
// line is debt, not a partial-line poison — so the owner's own error path
// handles it. Durable ops: Sync 6 (faulted), rollback Truncate 7 (faulted);
// barrier Sync 9 (faulted).
func TestAppendSyncedReturnsErrorWhenDurabilityCannotBeEstablished(t *testing.T) {
	plan := faultPlan(6)
	plan[7] = 0x00 // rollback Truncate fails: the line stays, unsynced
	plan[9] = 0x00 // barrier Sync fails: durability never established
	base := afero.NewMemMapFs()
	w, err := newWriterFS(fault.FS(base, fault.FromBytes(plan)), faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	if err := w.AppendSynced(schema.NewTurn(schema.TurnAssistant, llm.Assistant("recorded but never synced"))); err == nil {
		t.Fatal("AppendSynced reported success; durability was never established")
	} else if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("AppendSynced error = %v, want the injected barrier failure", err)
	}
	if w.Poisoned() {
		t.Fatal("an unsynced whole line poisoned the writer; only a partial line does")
	}
	// The record is in the file (a returning reader finds it); durability is
	// what was missing, and the next successful fsync would settle it.
	if entries := faultTestEntries(t, base); len(entries) != 1 {
		t.Fatalf("entries = %d, want the one retained record", len(entries))
	}
}

func decodeMemEntries(t *testing.T, fs afero.Fs, path string) []Entry {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var entries []Entry
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if entry, err := DecodeEntry(line); err == nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

// AppendSynced reports durability through its return, not the shared warning
// queue, so it must not drop a diagnostic another append left there. A buffered
// append whose sync failed queues its retained warning (ops Write 4, Sync 5);
// a following clean AppendSynced must leave that warning for the session to
// drain.
func TestAppendSyncedDoesNotDropAnotherAppendsQueuedWarning(t *testing.T) {
	base := afero.NewMemMapFs()
	w, err := newWriterFS(fault.FS(base, fault.FromBytes(faultPlan(5))), faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	// Buffered append: whole line lands, its fsync (op 5) fails → retained,
	// warning queued, writer usable.
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("buffered retained"))); err != nil {
		t.Fatalf("buffered Append error = %v, want nil", err)
	}
	// A following AppendSynced (clean, past the single faulted op) must not
	// touch the queued warning.
	if err := w.AppendSynced(schema.NewTurn(schema.TurnAssistant, llm.Assistant("synced clean"))); err != nil {
		t.Fatalf("AppendSynced error = %v, want nil", err)
	}
	warnings := w.DrainWarnings()
	if len(warnings) != 1 {
		t.Fatalf("queued warnings after AppendSynced = %d, want the buffered append's one, undropped", len(warnings))
	}
}

// AppendSynced raises its recovery barrier only for a retained append; a clean
// durable append is already synced and needs no second fsync. Durable append
// ops after the header: Seek 4, Write 5, Sync 6. If AppendSynced fsynced again
// it would be op 7 — faulting op 7 must not reach it, so the call succeeds.
func TestAppendSyncedDoesNotDoubleFsyncACleanAppend(t *testing.T) {
	base := afero.NewMemMapFs()
	w, err := newWriterFS(fault.FS(base, fault.FromBytes(faultPlan(7))), faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	if err := w.AppendSynced(schema.NewTurn(schema.TurnAssistant, llm.Assistant("clean and synced"))); err != nil {
		t.Fatalf("AppendSynced error = %v, want nil: a clean append needs no second barrier fsync", err)
	}
	if entries := faultTestEntries(t, base); len(entries) != 1 {
		t.Fatalf("entries = %d, want the one durable record", len(entries))
	}
}

// AppendSynced must let a durability owner tell "recorded but not durable" from
// "not recorded", or the owner discards and re-appends and the record lands
// twice. When the append's fsync, its rollback, and the recovery barrier all
// fail, the whole line is retained in the file: AppendSynced returns
// *RetainedUnsyncedError (errors.Is ErrRetainedUnsynced) carrying the seq, and
// the writer holds exactly one line — an owner that adopts it and does not
// re-append keeps the file duplicate-free. Durable ops: Seq 4, Write 5, Sync 6
// (fault), rollback Truncate 7 (fault); barrier Sync 9 (fault).
func TestAppendSyncedReportsRetainedUnsyncedForAdoptionWithoutDuplication(t *testing.T) {
	plan := faultPlan(6)
	plan[7] = 0x00 // rollback truncate fails: the whole line stays
	plan[9] = 0x00 // recovery barrier fsync fails: durability unestablished
	base := afero.NewMemMapFs()
	w, err := newWriterFS(fault.FS(base, fault.FromBytes(plan)), faultTranscriptPath, faultTestHeader(), true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	err = w.AppendSynced(schema.NewTurn(schema.TurnUserInput, llm.User("recorded, not durable")))
	if !errors.Is(err, ErrRetainedUnsynced) {
		t.Fatalf("AppendSynced error = %v, want ErrRetainedUnsynced so the owner adopts rather than re-appends", err)
	}
	var retained *RetainedUnsyncedError
	if !errors.As(err, &retained) || retained.Seq != 0 {
		t.Fatalf("AppendSynced error = %v, want a RetainedUnsyncedError carrying seq 0", err)
	}
	// An owner that heeds this does not re-append; the file holds one line.
	if entries := faultTestEntries(t, base); len(entries) != 1 {
		t.Fatalf("entries = %d, want the one retained record (an owner that re-appended here would duplicate it)", len(entries))
	}
	// The diagnostic is queued for the session to surface, not returned only.
	if len(w.DrainWarnings()) != 1 {
		t.Fatal("the retained record queued no warning for the session to surface")
	}
}

// AppendSynced fails closed on a closed writer: a durability owner writing
// during shutdown (closeAttachedTranscript closes the writer without clearing
// the session's reference) must not read a dropped write as durable. The nil
// no-op is only for a writer that never existed (a stateless session).
func TestAppendSyncedFailsClosedOnAClosedWriter(t *testing.T) {
	w, err := NewWriterWithFS(afero.NewMemMapFs(), "/closed.jsonl", Header{SessionID: "sess-closed"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.AppendSynced(schema.NewTurn(schema.TurnUserInput, llm.User("during shutdown"))); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("AppendSynced on a closed writer = %v, want ErrWriterClosed (a synced owner must not read a dropped write as durable)", err)
	}
	// A writer that never existed stays a nil no-op, so a stateless session's
	// owners do not error.
	var nilWriter *Writer
	if err := nilWriter.AppendSynced(schema.NewTurn(schema.TurnUserInput, llm.User("no writer"))); err != nil {
		t.Fatalf("AppendSynced on a nil writer = %v, want nil no-op", err)
	}
}

// The closed check must be made under appendBatch's lock, or a Close that lands
// between a caller's own unlocked check and the append leaves AppendSynced in
// the retained==nil branch, returning nil — durable — for a write that recorded
// nothing. This asserts the locked path directly: with failClosed the closed
// writer is ErrWriterClosed; without it, the silent nil no-op the ordinary
// doors depend on.
func TestAppendBatchFailClosedDistinguishesClosedFromRecorded(t *testing.T) {
	w, err := NewWriterWithFS(afero.NewMemMapFs(), "/closed-batch.jsonl", Header{SessionID: "sess-closed-batch"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("after close"))

	// Ordinary door (failClosed=false): a closed writer is a silent nil no-op.
	if _, _, retained, err := w.appendBatch([]schema.Turn{turn}, Placement{}, true, true, false); err != nil || retained != nil {
		t.Fatalf("ordinary appendBatch on a closed writer = (retained %v, err %v), want the silent nil no-op", retained, err)
	}
	// Synced owner (failClosed=true): a closed writer fails closed, so
	// AppendSynced never reads a dropped write as durable.
	if _, _, retained, err := w.appendBatch([]schema.Turn{turn}, Placement{}, true, false, true); !errors.Is(err, ErrWriterClosed) || retained != nil {
		t.Fatalf("synced appendBatch on a closed writer = (retained %v, err %v), want ErrWriterClosed", retained, err)
	}
}
