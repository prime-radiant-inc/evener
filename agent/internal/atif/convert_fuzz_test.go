package atif

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/transcript"
)

// FuzzATIFConvert drives ConvertTranscriptWithOptions — the package's real
// transcript→trajectory conversion seam, including the per-turn assistant
// conversion that json.Unmarshals tool-call arguments. Input is a JSON object
// {"header":..,"entries":[..]} decoded into the transcript types the converter
// consumes. Beyond no-panic it asserts the produced Trajectory always
// re-serializes (the ATIF output is a JSON document by contract) and that the
// schema version is stamped.
func FuzzATIFConvert(f *testing.F) {
	seeds := []string{
		`{"header":{"session_id":"s1","model":"gpt-5.5","profile_id":"openai"},"entries":[
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hi"}]}}},
{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]}}},
{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"out","is_error":false}}]}}}
]}`,
		`{"header":{},"entries":[{"kind":"entry","seq":0,"turn":{"kind":"SUMMARY","message":{"role":"assistant","content":[{"kind":"text","text":"sum"}]}}}]}`,
		`{"header":{},"entries":[]}`,
		`{}`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var doc struct {
			Header  transcript.Header    `json:"header"`
			Entries []transcript.Entry   `json:"entries"`
			Calls   []transcript.APICall `json:"api_calls"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return // not a transcript doc: no-panic floor proven, stop
		}

		traj := ConvertTranscriptWithOptions(doc.Header, doc.Entries, doc.Calls, Options{})

		if traj.SchemaVersion == "" {
			t.Fatalf("converted trajectory missing schema version")
		}
		if _, err := json.Marshal(traj); err != nil {
			t.Fatalf("trajectory does not re-marshal: %v", err)
		}
	})
}
