package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// readTranscriptLines reads all non-empty lines from a JSONL transcript file.
func readTranscriptLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open transcript file: %v", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			t.Fatal("unexpected blank line in transcript")
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript file: %v", err)
	}
	return lines
}

// --- Fix 1: seq increment after successful write ---

func TestWriter_SeqNotIncrementedOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewWriter(path, Header{
		SessionID: "sess-seq-fix",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Write one successful entry (seq 0).
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("first"))); err != nil {
		t.Fatalf("Append 0: %v", err)
	}

	// Close the underlying file to force the next write to fail.
	w.mu.Lock()
	w.file.Close()
	w.mu.Unlock()

	// This Append should fail (file is closed).
	err = w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("should fail")))
	if err == nil {
		t.Fatal("expected error from Append on closed file")
	}

	// Reopen the file so future writes succeed.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	w.mu.Lock()
	w.file = f
	w.mu.Unlock()

	// The next successful write should use seq 1 (not seq 2, which would
	// indicate the failed write incremented seq).
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("after failure"))); err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}

	// Read back and check seq numbers.
	lines := readTranscriptLines(t, path)
	// header + 2 successful entries
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	var entry0, entry1 Entry
	json.Unmarshal([]byte(lines[1]), &entry0)
	json.Unmarshal([]byte(lines[2]), &entry1)

	if entry0.Seq != 0 {
		t.Errorf("entry0 seq = %d, want 0", entry0.Seq)
	}
	if entry1.Seq != 1 {
		t.Errorf("entry1 seq = %d, want 1 (no gap from failed write)", entry1.Seq)
	}
}

func TestWriter_PeriodicSync_SkipsSyncWithinInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewWriter(path, Header{
		SessionID: "sess-sync-001",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Set a long sync interval so rapid writes don't trigger sync.
	w.SyncInterval = 1 * time.Hour

	// Write several entries rapidly.
	for i := 0; i < 5; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// The dirty flag should be true (writes happened but no sync since header).
	w.mu.Lock()
	dirty := w.dirty
	w.mu.Unlock()
	if !dirty {
		t.Error("expected dirty=true after writes within sync interval")
	}

	// Close should flush all data.
	w.Close()

	// All data should be present after Close (header + 5 entries).
	lines := readTranscriptLines(t, path)
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines (header + 5 entries), got %d", len(lines))
	}
}

func TestWriter_PeriodicSync_SyncsAfterIntervalExpires(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewWriter(path, Header{
		SessionID: "sess-sync-002",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Set a very short sync interval.
	w.SyncInterval = 1 * time.Millisecond

	// Write first entry.
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("first"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Wait for interval to expire.
	time.Sleep(5 * time.Millisecond)

	// Next write should trigger a sync because the interval has elapsed.
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("second"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// After the sync, dirty should be false.
	w.mu.Lock()
	dirty := w.dirty
	w.mu.Unlock()
	if dirty {
		t.Error("expected dirty=false after sync interval elapsed and write occurred")
	}
}

func TestWriter_PeriodicSync_ZeroIntervalSyncsEveryWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	w, err := NewWriter(path, Header{
		SessionID: "sess-sync-003",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Zero interval = sync every write (backward compat).
	w.SyncInterval = 0

	for i := 0; i < 3; i++ {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("msg %d", i)))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		// After each write with zero interval, dirty should be false.
		w.mu.Lock()
		dirty := w.dirty
		w.mu.Unlock()
		if dirty {
			t.Errorf("Append %d: expected dirty=false with zero SyncInterval", i)
		}
	}
}

func TestOpenWriter_SetsLastSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Create a transcript.
	w, err := NewWriter(path, Header{
		SessionID: "sess-sync-006",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("msg 0")))
	w.Close()

	// Reopen for resume.
	before := time.Now()
	w2, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w2.Close()

	// lastSync should be set to approximately now (not zero).
	w2.mu.Lock()
	ls := w2.lastSync
	w2.mu.Unlock()
	if ls.Before(before) {
		t.Errorf("lastSync = %v, expected >= %v", ls, before)
	}
}
