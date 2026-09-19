package transcript

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
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
