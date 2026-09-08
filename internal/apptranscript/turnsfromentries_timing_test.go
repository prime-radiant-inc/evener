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

const largeFixtureEntryCount = 20000

func writeLargeTranscriptFixture(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "large.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID:    "th_large",
		SystemPrompt: "You are Evener.",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// The fixture write only needs the bytes on disk, so skip the per-append
	// fsync the durability default pays.
	w.SyncInterval = time.Hour
	for i := range largeFixtureEntryCount {
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
	return path
}

func itemProjection(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
	return []appwire.ThreadItem{{Type: "userMessage", ID: turnID, TurnID: turnID, Text: turn.Message.Content[0].Text}}
}

func openLargeTranscriptEntries(t testing.TB, path string) (transcript.Header, []transcript.Entry) {
	t.Helper()
	rw, entries, err := transcript.OpenWriterForSession(path, "th_large")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return rw.Header(), entries
}

// TestItemTurnsFromEntriesLargeFixture verifies that the file and entries
// paths produce the same complete projection for a large transcript. Their
// relative speed is reported by the benchmark below; wall-clock ratios are
// too dependent on machine scheduling for a test assertion.
func TestItemTurnsFromEntriesLargeFixture(t *testing.T) {
	path := writeLargeTranscriptFixture(t)
	header, entries := openLargeTranscriptEntries(t, path)
	fileTurns, err := ItemTurnsFromFile(path, 1<<30, itemProjection)
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	entryTurns, err := ItemTurnsFromEntries(header, entries, itemProjection)
	if err != nil {
		t.Fatalf("ItemTurnsFromEntries: %v", err)
	}
	if len(fileTurns) != largeFixtureEntryCount+1 {
		t.Fatalf("want header and %d user turns, got %d turns", largeFixtureEntryCount, len(fileTurns))
	}
	if !reflect.DeepEqual(fileTurns, entryTurns) {
		t.Fatalf("file and entries projections diverge: file=%d entries=%d", len(fileTurns), len(entryTurns))
	}
}

func BenchmarkItemTurnsFromEntriesLargeFixture(b *testing.B) {
	path := writeLargeTranscriptFixture(b)
	header, entries := openLargeTranscriptEntries(b, path)
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(info.Size()), "fixture-bytes")
	b.Run("file", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := ItemTurnsFromFile(path, 1<<30, itemProjection); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("entries", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := ItemTurnsFromEntries(header, entries, itemProjection); err != nil {
				b.Fatal(err)
			}
		}
	})
}
