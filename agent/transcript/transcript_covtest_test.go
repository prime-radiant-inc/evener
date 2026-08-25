package transcript

import (
	"errors"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestTrackFailures_NilWriter covers the nil-writer guard in TrackFailures
// (line 256-258).
func TestTrackFailures_NilWriter(t *testing.T) {
	var w *Writer
	w.TrackFailures(nil, 0)                      // should not panic
	w.TrackFailures([]Entry{{Kind: "entry"}}, 0) // should not panic
}

// TestEstablishDurability_NilWriter covers the nil/nil-file guard in
// EstablishDurability (line 376-378).
func TestEstablishDurability_NilWriter(t *testing.T) {
	var w *Writer
	if err := w.EstablishDurability(); err == nil {
		t.Error("EstablishDurability on nil writer should return error")
	}
}

// TestEstablishDurability_ClosedWriter covers the closed-writer guard in
// EstablishDurability (line 381-383).
func TestEstablishDurability_ClosedWriter(t *testing.T) {
	fs := afero.NewMemMapFs()
	w, err := newWriterFS(fs, "/session/transcript.jsonl", Header{
		SessionID: "test",
		CreatedAt: time.Unix(0, 0).UTC(),
		ProfileID: "openai",
		Model:     "gpt-5.5",
	}, true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.EstablishDurability(); err == nil {
		t.Error("EstablishDurability on closed writer should return error")
	}
}

// TestOpenWriterForSessionWithFS_OpenError covers the open-error path in
// OpenWriterForSessionWithFS (line 547-548).
func TestOpenWriterForSessionWithFS_OpenError(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, _, err := OpenWriterForSessionWithFS(fs, "/nonexistent/path/transcript.jsonl", "sid")
	if err == nil {
		t.Error("OpenWriterForSessionWithFS on nonexistent path should return error")
	}
}

// TestDecodeEntry_MultipleJSONValues covers the "multiple JSON values" error
// path in the shared decode helper (line 165-166).
func TestDecodeEntry_MultipleJSONValues(t *testing.T) {
	// An entry with trailing JSON should be rejected.
	_, err := DecodeEntry([]byte(`{"kind":"entry","turn":{}}{"kind":"entry","turn":{}}`))
	if err == nil {
		t.Error("DecodeEntry with multiple JSON values should return error")
	}
}

// TestResumeWriter_BlankLines covers the blank-line skip path in resumeWriter
// (line 594-596): a transcript with blank lines between entries should skip
// them without error.
func TestResumeWriter_BlankLines(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/session/transcript.jsonl"
	w, err := newWriterFS(fs, path, Header{
		SessionID: "sid",
		CreatedAt: time.Unix(0, 0).UTC(),
		ProfileID: "openai",
		Model:     "gpt-5.5",
	}, true)
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hello"}},
	})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Read the file, insert a blank line after the header, and rewrite.
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitTranscriptLines(data)
	newContent := lines[0] + "\n\n" + lines[1] + "\n"
	if err := afero.WriteFile(fs, path, []byte(newContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// Resume — the blank line should be skipped.
	w2, entries, err := OpenWriterForSessionWithFS(fs, path, "sid")
	if err != nil {
		t.Fatalf("OpenWriterForSessionWithFS: %v", err)
	}
	defer w2.Close()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (blank line skipped), got %d", len(entries))
	}
}

// TestResumeWriter_MissingHeader covers the missing-header error path in
// resumeWriter (line 625-626): a transcript with entries but no header should
// return ErrUnsupportedFormat.
func TestResumeWriter_MissingHeader(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/session/transcript.jsonl"
	// Write only an entry, no header.
	content := `{"kind":"entry","seq":1,"turn":{"kind":"user_input","message":{"role":"user","content":[{"kind":"text","text":"hi"}]}}}` + "\n"
	if err := afero.WriteFile(fs, path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := OpenWriterForSessionWithFS(fs, path, "")
	if err == nil {
		t.Error("OpenWriterForSessionWithFS without header should return error")
	}
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("expected ErrUnsupportedFormat, got: %v", err)
	}
}

// splitTranscriptLines splits a transcript file's bytes into non-empty lines.
func splitTranscriptLines(data []byte) []string {
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := string(data[start:i])
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		line := string(data[start:])
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
