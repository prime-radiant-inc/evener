package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestNewWriterEmitsFormatVersionTwoAndSemanticEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path, Header{SessionID: "sess_v2"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readTranscriptLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want header and one entry", len(lines))
	}
	var header Header
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.Kind != "header" || header.FormatVersion != FormatVersion || FormatVersion != 2 {
		t.Fatalf("header = kind %q version %d, FormatVersion %d; want header version 2", header.Kind, header.FormatVersion, FormatVersion)
	}
	var entry Entry
	if err := json.Unmarshal([]byte(lines[1]), &entry); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if entry.Kind != "entry" || entry.Seq != 0 {
		t.Fatalf("entry = kind %q seq %d, want entry seq 0", entry.Kind, entry.Seq)
	}
}

func TestOpenWriterRejectsUnsupportedTranscriptFormat(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantUnsupported bool
	}{
		{"version one", `{"kind":"header","format_version":1}` + "\n", true},
		{"missing version", `{"kind":"header"}` + "\n", true},
		{"mixed api call", `{"kind":"header","format_version":2}` + "\n" + `{"kind":"api_call"}` + "\n", true},
		{"unknown record", `{"kind":"header","format_version":2}` + "\n" + `{"kind":"mystery"}` + "\n", true},
		{"duplicate header", `{"kind":"header","format_version":2}` + "\n" + `{"kind":"header","format_version":2}` + "\n", true},
		{"entry before header", `{"kind":"entry","seq":0,"turn":{}}` + "\n", true},
		{"corrupt interior", `{"kind":"header","format_version":2}` + "\n" + "{not json}\n" + `{"kind":"entry","seq":0,"turn":{}}` + "\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			w, err := OpenWriter(path)
			if w != nil {
				_ = w.Close()
			}
			if tc.wantUnsupported {
				if !errors.Is(err, ErrUnsupportedFormat) {
					t.Fatalf("OpenWriter error = %v, want ErrUnsupportedFormat", err)
				}
			} else if err == nil {
				t.Fatal("OpenWriter error = nil, want corrupt interior error")
			}
		})
	}
}

func TestOpenWriterToleratesOnlyIncompleteFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := `{"kind":"header","format_version":2}` + "\n" +
		`{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}}` + "\n" +
		`{"kind":"entry"`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("ok"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	lines := readTranscriptLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want header and two semantic entries", len(lines))
	}
	var appended Entry
	if err := json.Unmarshal([]byte(lines[2]), &appended); err != nil {
		t.Fatalf("decode appended entry: %v", err)
	}
	if appended.Seq != 1 {
		t.Fatalf("appended seq = %d, want 1", appended.Seq)
	}
}

func TestOpenWriterRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.jsonl")
	path := filepath.Join(dir, "transcript.jsonl")
	body := `{"kind":"header","format_version":2,"session_id":"session-a"}` + "\npartial-tail"
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	w, err := OpenWriter(path)
	if w != nil {
		_ = w.Close()
	}
	if err == nil {
		t.Fatal("OpenWriter followed a symlink")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile target: %v", readErr)
	}
	if string(got) != body {
		t.Fatalf("symlink target changed: got %q want %q", got, body)
	}
}

func TestTranscriptAuthorshipStrictRoundtrip(t *testing.T) {
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("author-sentinel"))
	turn.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "sess_author", OwnerSessionID: "sess_owner", DelegateID: "dlg_author", DelegateName: "Author"}, Inherited: true}
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path, Header{SessionID: "sess_author"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(turn); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readTranscriptLines(t, path)
	entry, err := DecodeEntry([]byte(lines[1]))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Turn.Authorship == nil || *entry.Turn.Authorship != *turn.Authorship {
		t.Fatalf("authorship = %#v", entry.Turn.Authorship)
	}
	for _, raw := range []string{
		`{"kind":"entry","seq":0,"turn":{"kind":"ASSISTANT","authorship":{"author":{"kind":"agent","extra":1},"inherited":false}}}`,
		`{"kind":"entry","seq":0,"turn":{"kind":"ASSISTANT","authorship":{"author":{"kind":2},"inherited":false}}}`,
	} {
		if _, err := DecodeEntry([]byte(raw)); err == nil {
			t.Fatal("strict reader accepted malformed authorship")
		}
	}
}

// TestOpenWriterForSessionWithFS_OpenError covers the open-error path in
// OpenWriterForSessionWithFS.
func TestOpenWriterForSessionWithFS_OpenError(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, _, err := OpenWriterForSessionWithFS(fs, "/nonexistent/path/transcript.jsonl", "sid")
	if err == nil {
		t.Error("OpenWriterForSessionWithFS on nonexistent path should return error")
	}
}

// TestResumeWriter_BlankLines covers the blank-line skip path in resumeWriter
// : a transcript with blank lines between entries should skip
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
// resumeWriter: a transcript with entries but no header should
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
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(line) > 0 {
			lines = append(lines, string(line))
		}
	}
	return lines
}

// TestOpenTranscriptAppendFile_NonRegular covers the non-regular-file branch: opening a device special file (which succeeds with
// O_RDWR|O_NOFOLLOW) and then detecting it is not a regular file.
func TestOpenTranscriptAppendFile_NonRegular(t *testing.T) {
	// /dev/null is a character device — open succeeds, but Stat().IsRegular()
	// returns false, triggering the "not a regular file" error.
	_, err := openTranscriptAppendFile("/dev/null")
	if err == nil {
		t.Fatal("expected error opening non-regular file as transcript")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want 'not a regular file'", err)
	}
}

// TestOpenTranscriptAppendFile_NotExist covers the open-error branch: a missing path returns an error.
func TestOpenTranscriptAppendFile_NotExist(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.jsonl")
	_, err := openTranscriptAppendFile(missing)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "no such file or directory") {
		t.Fatalf("error = %v, want error containing 'no such file or directory'", err)
	}
}
