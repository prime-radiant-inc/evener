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
// is in the file and no seq is spent, so a retry lands the whole batch at seq 0.
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
	if firstSeq != 0 {
		t.Fatalf("retry firstSeq = %d, want 0: the failed batch spent no seq", firstSeq)
	}
	entries := faultTestEntries(t, base)
	if len(entries) != 2 || entries[0].Seq != 0 || entries[1].Seq != 1 {
		t.Fatalf("entries after the retry = %+v, want two at seq 0 and 1", entries)
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
