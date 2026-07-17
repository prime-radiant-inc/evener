package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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
