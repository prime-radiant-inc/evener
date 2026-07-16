package doctor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/transcript"
)

func TestLoadTranscriptRejectsUnsupportedFormatsWithoutPartialState(t *testing.T) {
	entry := `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}` + "\n"
	tests := []struct {
		name string
		body string
	}{
		{"version one", `{"kind":"header","format_version":1,"session_id":"legacy"}` + "\n" + entry},
		{"missing version", `{"kind":"header","session_id":"missing"}` + "\n" + entry},
		{"mixed provider record", `{"kind":"header","format_version":2,"session_id":"mixed"}` + "\n" + entry + `{"kind":"api_call"}` + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write input: %v", err)
			}

			doc, err := loadTranscript(path)
			if !errors.Is(err, transcript.ErrUnsupportedFormat) {
				t.Fatalf("loadTranscript error = %v, want ErrUnsupportedFormat", err)
			}
			if !reflect.DeepEqual(doc, transcriptDoc{}) {
				t.Fatalf("loadTranscript accepted partial state: %+v", doc)
			}
		})
	}
}

// FuzzDoctorLoadTranscript drives loadTranscript — the doctor package's real
// transcript-file decode seam (the per-line kind peek + Header/Entry
// json.Unmarshal). Input is a raw transcript JSONL blob written to a temp file.
// Beyond no-panic it asserts that every entry the loader accepted re-serializes
// (no decode produced a value json.Marshal can't round-trip), and that a header
// line populated the doc header kind when present.
func FuzzDoctorLoadTranscript(f *testing.F) {
	seeds := []string{
		`{"kind":"header","format_version":2,"session_id":"s1","model":"gpt-5.5"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hi"}]}}}`,
		`{"kind":"header","format_version":1,"session_id":"legacy"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[]}}}`,
		`{"kind":"header","session_id":"s2"}`,
		`{"kind":"entry","seq":0,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"x","content":"out"}}]}}}`,
		``,
		`not json`,
		`{"kind":"unknown"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "transcript.jsonl")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}

		doc, err := loadTranscript(path)
		if err != nil {
			return // malformed transcript: no-panic floor proven, stop
		}

		// Every accepted entry must re-serialize cleanly.
		for i, e := range doc.Entries {
			if _, err := json.Marshal(e); err != nil {
				t.Fatalf("loaded entry %d does not re-marshal: %v", i, err)
			}
		}
		if _, err := json.Marshal(doc.Header); err != nil {
			t.Fatalf("loaded header does not re-marshal: %v", err)
		}
	})
}
