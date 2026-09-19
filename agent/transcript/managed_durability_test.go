package transcript

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/fuzz/fault"
	"primeradiant.com/evener/llm"
)

type indexedSyncedWriter interface {
	AppendSyncedEntry(schema.Turn) (int, error)
}

func TestManagedAppendSyncedEntryRequiresWriterAndReportsActualPosition(t *testing.T) {
	var missing *Writer
	strict, ok := any(missing).(indexedSyncedWriter)
	if !ok {
		t.Fatal("writer has no strict indexed durability door")
	}
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("anchor"))
	if _, err := strict.AppendSyncedEntry(turn); err == nil {
		t.Fatal("nil writer acknowledged durable anchor")
	}
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriterWithFS(afero.NewOsFs(), path, Header{SessionID: "managed-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("input"))); err != nil {
		t.Fatal(err)
	}
	seq, err := any(w).(indexedSyncedWriter).AppendSyncedEntry(turn)
	if err != nil || seq != 1 {
		t.Fatalf("anchor position=%d err=%v", seq, err)
	}
	entries := decodeMemEntries(t, afero.NewOsFs(), path)
	if len(entries) != 2 || entries[1].Seq != seq {
		t.Fatalf("recorded entries=%+v", entries)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := any(w).(indexedSyncedWriter).AppendSyncedEntry(turn); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("closed writer: %v", err)
	}
}

func TestManagedAppendSyncedEntryHardErrorReturnsZeroAfterRollback(t *testing.T) {
	plan := faultPlan(8) // prefix Write 4/Sync 5; durable append Seek 6/Write 7/Sync 8.
	path := filepath.Join(t.TempDir(), "session", "transcript.jsonl")
	w, err := newWriterFS(fault.FS(afero.NewOsFs(), fault.FromBytes(plan)), path, Header{SessionID: "managed-hard-error"}, true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("prefix"))); err != nil {
		t.Fatalf("prefix append: %v", err)
	}
	before := readOSIndexedEntries(t, path)

	seq, err := w.AppendSyncedEntry(schema.NewTurn(schema.TurnAssistant, llm.Assistant("rolled back")))
	if err == nil {
		t.Fatal("faulted durable append returned nil")
	}
	if errors.Is(err, ErrRetainedUnsynced) {
		t.Fatalf("hard append error = %v, want ordinary error after rollback", err)
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("hard append error = %v, want the injected sync failure", err)
	}
	if seq != 0 {
		t.Fatalf("hard append position = %d, want zero when no record was appended", seq)
	}
	if after := readOSIndexedEntries(t, path); !reflect.DeepEqual(after, before) {
		t.Fatalf("entries changed after rolled-back append: before=%+v after=%+v", before, after)
	}

	next, err := w.AppendSyncedEntry(schema.NewTurn(schema.TurnAssistant, llm.Assistant("next")))
	if err != nil {
		t.Fatalf("append after rollback: %v", err)
	}
	if next != 1 {
		t.Fatalf("append after rollback position = %d, want the unspent next sequence 1", next)
	}
	entries := readOSIndexedEntries(t, path)
	if len(entries) != 2 || entries[1].Seq != next {
		t.Fatalf("entries after retry = %+v, want prefix plus seq %d", entries, next)
	}
}

func TestManagedAppendSyncedEntryRetainedPositionAndRecovery(t *testing.T) {
	plan := faultPlan(8)
	plan[9] = 0x00  // rollback Truncate fails, retaining the whole record.
	plan[11] = 0x00 // recovery barrier Sync fails.
	path := filepath.Join(t.TempDir(), "session", "transcript.jsonl")
	w, err := newWriterFS(fault.FS(afero.NewOsFs(), fault.FromBytes(plan)), path, Header{SessionID: "managed-retained"}, true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("prefix"))); err != nil {
		t.Fatalf("prefix append: %v", err)
	}

	seq, err := w.AppendSyncedEntry(schema.NewTurn(schema.TurnAssistant, llm.Assistant("retained")))
	if !errors.Is(err, ErrRetainedUnsynced) {
		t.Fatalf("retained append error = %v, want ErrRetainedUnsynced", err)
	}
	var retained *RetainedUnsyncedError
	if !errors.As(err, &retained) {
		t.Fatalf("retained append error = %v, want RetainedUnsyncedError", err)
	}
	entries := readOSIndexedEntries(t, path)
	if len(entries) != 2 {
		t.Fatalf("entries after retained append = %+v, want prefix plus exactly one new record", entries)
	}
	if seq != retained.Seq || seq != entries[1].Seq || seq != 1 {
		t.Fatalf("retained identity: returned=%d typed=%d file=%d, want all sequence 1", seq, retained.Seq, entries[1].Seq)
	}

	if err := w.EstablishDurability(); err != nil {
		t.Fatalf("establish durability after one-shot fault: %v", err)
	}
	next, err := w.AppendSyncedEntry(schema.NewTurn(schema.TurnAssistant, llm.Assistant("next")))
	if err != nil {
		t.Fatalf("append after retained recovery: %v", err)
	}
	if next != 2 {
		t.Fatalf("append after retained recovery position = %d, want 2", next)
	}
	entries = readOSIndexedEntries(t, path)
	if len(entries) != 3 || entries[2].Seq != next {
		t.Fatalf("entries after retained recovery = %+v, want seqs 0, 1, 2", entries)
	}
}

func readOSIndexedEntries(t *testing.T, path string) []Entry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) == 0 {
		t.Fatal("transcript is empty")
	}
	if _, err := DecodeHeader(bytes.TrimSpace(lines[0])); err != nil {
		t.Fatalf("decode transcript header: %v", err)
	}
	entries := make([]Entry, 0, len(lines)-1)
	for _, line := range lines[1:] {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		entry, err := DecodeEntry(line)
		if err != nil {
			t.Fatalf("decode transcript entry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}
