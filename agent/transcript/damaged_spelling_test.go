package transcript

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The writer only produces a string arguments_spelling, so these tests write
// through it, then swap the placeholder for an object-form value on disk.
const (
	damagedSpellingPlaceholder = "DAMAGED_SPELLING_PLACEHOLDER"
	spelledArguments           = `{"path":"a.png","intent":"read a.png"}`
	spelledDigest              = `0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef`
)

func spelledToolCallTurn() schema.Turn {
	return schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:                "call_spelling",
			Name:              "read_file",
			Arguments:         json.RawMessage(spelledArguments),
			ArgumentsSpelling: llm.Spelling(damagedSpellingPlaceholder),
		},
	}}})
}

// replaceSpelling swaps the marshaled placeholder token for the raw spelling
// value, so the bytes carry spelling exactly as given.
func replaceSpelling(t *testing.T, data []byte, spelling string) []byte {
	t.Helper()
	token := []byte(`"arguments_spelling":"` + damagedSpellingPlaceholder + `"`)
	if n := bytes.Count(data, token); n != 1 {
		t.Fatalf("data carries %d spelling placeholders, want 1", n)
	}
	return bytes.Replace(data, token, []byte(`"arguments_spelling":`+spelling), 1)
}

// writeSpelledFile writes one transcript whose tool call carries spelling.
func writeSpelledFile(t *testing.T, spelling string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "damaged-spelling.transcript.jsonl")
	w, err := NewWriter(path, Header{SessionID: "034DamagedSpelling"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Record(spelledToolCallTurn(), RecordOptions{Door: DoorBuffered}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if err := os.WriteFile(path, replaceSpelling(t, data, spelling), 0o600); err != nil {
		t.Fatalf("rewrite transcript: %v", err)
	}
	return path
}

// TestColdOpenToleratesDamagedOptionalArgumentsSpelling pins the invariant:
// damage to any optional spelling field must not quarantine a record whose
// structured arguments are intact. Each case cold-opens a real file through
// OpenWriterForSession and gets the replayed arguments back byte for byte. The
// final subtest holds that tolerance to the spelling field.
func TestColdOpenToleratesDamagedOptionalArgumentsSpelling(t *testing.T) {
	cases := []struct {
		name     string
		spelling string
	}{
		{"damaged digest", `{"sha256":17,"edits":[{"at":0,"bytes":"IA=="}]}`},
		{"damaged edit offset", `{"sha256":"` + spelledDigest + `","edits":[{"at":1.5,"bytes":"IA=="}]}`},
		{"damaged edit payload", `{"sha256":"` + spelledDigest + `","edits":[{"at":0,"bytes":"!!not-base64!!"}]}`},
		{"edits not an array", `{"sha256":"` + spelledDigest + `","edits":{"at":0}}`},
		{"primitive spelling", `17`},
		{"array spelling", `[]`},
		{"null spelling", `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const sessionID = "034DamagedSpelling"
			resumed, entries, err := OpenWriterForSession(writeSpelledFile(t, tc.spelling), sessionID)
			if err != nil {
				t.Fatalf("OpenWriterForSession over a %s: %v", tc.name, err)
			}
			defer func() { _ = resumed.Close() }()
			if len(entries) != 1 {
				t.Fatalf("entries = %d, want the one spelled entry", len(entries))
			}
			parts := entries[0].Turn.Message.Content
			if len(parts) != 1 || parts[0].ToolCall == nil {
				t.Fatalf("content = %+v, want the one tool call", parts)
			}
			if got := parts[0].ToolCall.SentArguments(); got != spelledArguments {
				t.Fatalf("SentArguments() = %q, want the intact structured %q", got, spelledArguments)
			}
			if got, wire := parts[0].ToolCall.ArgumentsSpelling, parts[0].ToolCall.WireArguments(); got != "" || wire != spelledArguments {
				t.Fatalf("spelling = %q, WireArguments() = %q, want discarded and %q", got, wire, spelledArguments)
			}
		})
	}

	t.Run("unknown member still rejected", func(t *testing.T) {
		path := writeSpelledFile(t, `{"sha256":17,"edits":[{"at":1.5,"bytes":"!!"}]}`)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read transcript: %v", err)
		}
		unknown := bytes.Replace(data, []byte(`"tool_call":{`), []byte(`"tool_call":{"argumnts":{},`), 1)
		if bytes.Equal(unknown, data) {
			t.Fatalf("no tool_call object to wrap: %s", data)
		}
		if err := os.WriteFile(path, unknown, 0o600); err != nil {
			t.Fatalf("rewrite transcript: %v", err)
		}
		if _, _, err := OpenWriterForSession(path, "034DamagedSpelling"); err == nil {
			t.Fatalf("OpenWriterForSession accepted an unknown member beside arguments")
		}
	})
}
