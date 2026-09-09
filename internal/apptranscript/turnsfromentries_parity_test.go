package apptranscript

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func largeEntryProjectionFixture(t testing.TB) (string, transcript.Header, []transcript.Entry) {
	t.Helper()
	const entryCount = 20000
	path := filepath.Join(t.TempDir(), "large.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID:    "th_large",
		SystemPrompt: "You are Evener.",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// The measurement is the READ side; the fixture write only needs the
	// bytes on disk, so skip the per-append fsync the durability default pays.
	w.SyncInterval = time.Hour
	for i := range entryCount {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("message %d with some body text to make the line realistic", i)))
		turn.Usage = llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		turn.Timestamp = time.Unix(1_700_000_000+int64(i), 0).UTC()
		if err := w.Append(turn); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rw, entries, err := transcript.OpenWriterForSession(path, "th_large")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	return path, rw.Header(), entries
}

func largeEntryProjector(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
	return []appwire.ThreadItem{{Type: "userMessage", ID: turnID, TurnID: turnID, Text: turn.Message.Content[0].Text}}
}

func TestItemTurnsFromEntriesLargeFixtureParity(t *testing.T) {
	path, header, entries := largeEntryProjectionFixture(t)
	fileTurns, err := ItemTurnsFromFile(path, 1<<30, largeEntryProjector)
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	entryTurns, err := ItemTurnsFromEntries(header, entries, largeEntryProjector)
	if err != nil {
		t.Fatalf("ItemTurnsFromEntries: %v", err)
	}
	if len(entryTurns) != len(entries)+1 {
		t.Fatalf("got %d turns for %d entries and system prelude", len(entryTurns), len(entries))
	}
	if !reflect.DeepEqual(fileTurns, entryTurns) {
		t.Fatal("file and in-memory projections differ")
	}
}

// Benchmark the saved-file scan and the already-decoded projection separately;
// scheduler load can change their timing ratio without changing either contract.
func BenchmarkItemTurnsLargeFixture(b *testing.B) {
	path, header, entries := largeEntryProjectionFixture(b)
	b.Run("file", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ItemTurnsFromFile(path, 1<<30, largeEntryProjector); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("entries", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ItemTurnsFromEntries(header, entries, largeEntryProjector); err != nil {
				b.Fatal(err)
			}
		}
	})
}
