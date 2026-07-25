package transcript

import (
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func newFailureTestWriter(t *testing.T, path string) *Writer {
	t.Helper()
	w, err := NewWriter(path, Header{
		SessionID: "sess-failures",
		CreatedAt: time.Now().UTC(),
		ProfileID: "test",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return w
}

func TestWriterReportsNoCountUntilItIsAskedToTrack(t *testing.T) {
	// Absent and zero are different claims. A writer nobody installed a counter
	// on has measured nothing, and must say so rather than report a clean 0 —
	// a producer that forgets to track has to fall silent, not vouch.
	w := newFailureTestWriter(t, filepath.Join(t.TempDir(), "transcript.jsonl"))
	defer w.Close()

	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if count, ok := w.FailedToolCalls(); ok {
		t.Fatalf("FailedToolCalls() = (%d, true), want absent on an untracked writer", count)
	}
}

func TestWriterCountsFailuresAsTheyAreWritten(t *testing.T) {
	w := newFailureTestWriter(t, filepath.Join(t.TempDir(), "transcript.jsonl"))
	defer w.Close()
	w.TrackFailures(nil, 0)

	if count, ok := w.FailedToolCalls(); !ok || count != 0 {
		t.Fatalf("FailedToolCalls() = (%d, %t), want a measured 0 before anything is written", count, ok)
	}
	if err := w.Append(toolCallTurn("call_1", "shell")); err != nil {
		t.Fatalf("Append call: %v", err)
	}
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", ToolState: exitState(1)})); err != nil {
		t.Fatalf("Append result: %v", err)
	}
	if count, ok := w.FailedToolCalls(); !ok || count != 1 {
		t.Fatalf("FailedToolCalls() = (%d, %t), want (1, true) right after the failure landed", count, ok)
	}
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_2", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append second result: %v", err)
	}
	if count, _ := w.FailedToolCalls(); count != 2 {
		t.Fatalf("FailedToolCalls() = %d, want 2", count)
	}
}

func TestWriterSeedsFromTheEntriesAlreadyOnDisk(t *testing.T) {
	// A resumed session's earlier failures are on disk and nowhere in memory
	// (compaction rewrites history). Seeding from the entries the resume read
	// is what makes the live figure whole-session rather than since-restart.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w := newFailureTestWriter(t, path)
	w.TrackFailures(nil, 0)
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, entries, err := OpenWriterForSession(path, "sess-failures")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	defer reopened.Close()
	reopened.TrackFailures(entries, 0)

	if count, ok := reopened.FailedToolCalls(); !ok || count != 1 {
		t.Fatalf("FailedToolCalls() = (%d, %t), want (1, true) seeded from the transcript", count, ok)
	}
	if err := reopened.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_2", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append after resume: %v", err)
	}
	if count, _ := reopened.FailedToolCalls(); count != 2 {
		t.Fatalf("FailedToolCalls() = %d, want 2 (seed plus this run)", count)
	}
}

func TestWriterKeepsItsCountAfterClose(t *testing.T) {
	// A session that ends while someone is watching keeps serving its thread
	// from the daemon until the next read reroutes to disk. The final count has
	// to survive Close or the figure blinks out exactly at the moment it settles.
	w := newFailureTestWriter(t, filepath.Join(t.TempDir(), "transcript.jsonl"))
	w.TrackFailures(nil, 0)
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if count, ok := w.FailedToolCalls(); !ok || count != 1 {
		t.Fatalf("FailedToolCalls() after Close = (%d, %t), want (1, true)", count, ok)
	}
}

func TestWriterDoesNotCountAnEntryThatFailedToLand(t *testing.T) {
	// The count is a statement about the transcript, so it may only move for
	// bytes that actually reached it — an append that errored (and rolled back)
	// leaves no glyph to agree with.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w := newFailureTestWriter(t, path)
	w.TrackFailures(nil, 0)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A closed writer drops appends on the floor; nothing lands, nothing counts.
	if err := w.Append(toolResultTurn(llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", IsError: true})); err != nil {
		t.Fatalf("Append after close: %v", err)
	}
	if count, _ := w.FailedToolCalls(); count != 0 {
		t.Fatalf("FailedToolCalls() = %d, want 0: the entry never reached the transcript", count)
	}
}
