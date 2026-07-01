package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func w2tailWriteTranscript(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A missing file surfaces the open error rather than a corruption error.
func TestW2Tail_readStrictChildTranscript_OpenError(t *testing.T) {
	_, err := readStrictChildTranscript(filepath.Join(t.TempDir(), "nope.jsonl"), "01SESS", 0)
	if err == nil || errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("open error = %v, want a plain open failure", err)
	}
}

// A truncated final line (no trailing newline, invalid JSON) is tolerated: the
// partial record is skipped and the read succeeds.
func TestW2Tail_readStrictChildTranscript_FinalIncompleteLine(t *testing.T) {
	content := "{\"kind\":\"header\",\"session_id\":\"01SESS\"}\n{trunc"
	data, err := readStrictChildTranscript(w2tailWriteTranscript(t, content), "01SESS", 0)
	if err != nil {
		t.Fatalf("final-incomplete should be tolerated: %v", err)
	}
	if data.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", data.Skipped)
	}
}

// A truncated final entry (valid kind, incomplete body, no newline) is also
// skipped rather than treated as corruption.
func TestW2Tail_readStrictChildTranscript_FinalIncompleteEntry(t *testing.T) {
	content := "{\"kind\":\"header\",\"session_id\":\"01SESS\"}\n{\"kind\":\"entry\",\"turn\":"
	data, err := readStrictChildTranscript(w2tailWriteTranscript(t, content), "01SESS", 0)
	if err != nil {
		t.Fatalf("final-incomplete entry should be tolerated: %v", err)
	}
	if data.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", data.Skipped)
	}
}

// A header line longer than the byte cap is reported as corrupt.
func TestW2Tail_readStrictChildTranscript_LineTooLong(t *testing.T) {
	content := "{\"kind\":\"header\",\"session_id\":\"01SESS\"}\n"
	_, err := readStrictChildTranscript(w2tailWriteTranscript(t, content), "01SESS", 10)
	if !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("err = %v, want corrupt (line too long)", err)
	}
}
