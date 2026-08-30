package transcript

import (
	"path/filepath"
	"testing"
)

// TestOpenWriterForSessionHeaderOnlyReturnsNonNilEntries pins the resume
// contract at the transcript layer: a successful open over a header-only
// file returns a NON-NIL empty entry slice. Session.RestoredTranscript
// (whose ok flag means "a transcript was opened") and the delegate
// attention fold (whose nil gate means "caller holds no decoded list")
// both key on the slice's nilness, so this must not regress to var-style
// nil initialization even though the two are equivalent to range and
// append.
func TestOpenWriterForSessionHeaderOnlyReturnsNonNilEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "header-only.transcript.jsonl")
	const sessionID = "034FwfafTaAYeTEUVT9lSR"
	writer, err := NewWriter(path, Header{SessionID: sessionID, ProfileID: "openai", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	resumed, entries, err := OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	defer func() { _ = resumed.Close() }()
	if entries == nil {
		t.Fatal("entries = nil over a header-only transcript, want non-nil empty: ok sentinels key on nilness")
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0 over a header-only transcript", len(entries))
	}
}
