package apptranscript

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestTurnsFromEntriesLargeFixtureTiming times the file read against the
// in-memory entries projection over a large synthetic transcript and gates
// on a generous ratio floor: the file form (scan + decode + project) must be
// at least 3x slower than the entries form (project only). The floor is
// deliberately loose so machine load cannot flake it — a regression that
// erases the entries form's win has to cost more than 3x before this fails.
func TestTurnsFromEntriesLargeFixtureTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("timing measurement, not a correctness gate")
	}
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

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	project := func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		return []appwire.ThreadItem{{Type: "userMessage", ID: turnID, TurnID: turnID, Text: turn.Message.Content[0].Text}}
	}

	// File form.
	fileStart := time.Now()
	fileTurns, err := TurnsFromFile(path, 1<<30, project)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	fileElapsed := time.Since(fileStart)

	// Entries form: decode once the way resume does, then project.
	rw, entries, err := transcript.OpenWriterForSession(path, "th_large")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	_ = rw.Close() //nolint:errcheck // measurement fixture
	entriesStart := time.Now()
	entryTurns, err := TurnsFromEntries(rw.Header(), entries, project)
	if err != nil {
		t.Fatalf("TurnsFromEntries: %v", err)
	}
	entriesElapsed := time.Since(entriesStart)

	t.Logf("fixture: %d entries, %d bytes (%.1f MB)", entryCount, info.Size(), float64(info.Size())/1024/1024)
	t.Logf("file form (scan+decode+project): %v, turns=%d", fileElapsed, len(fileTurns))
	t.Logf("entries form (project only):     %v, turns=%d", entriesElapsed, len(entryTurns))
	ratio := float64(fileElapsed) / float64(entriesElapsed)
	t.Logf("ratio: %.1fx", ratio)
	if ratio < 3 {
		t.Fatalf("file form was only %.1fx slower than the entries form; the entries projection's skip of the file I/O + decode pass is its entire reason to exist", ratio)
	}
	if len(fileTurns) != len(entryTurns) {
		t.Fatalf("turn counts diverge: file=%d entries=%d", len(fileTurns), len(entryTurns))
	}
}
