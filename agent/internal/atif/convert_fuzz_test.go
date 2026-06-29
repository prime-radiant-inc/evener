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
		// A maximal transcript: every turn kind, a responses-API api_call carrying
		// metadata, an assistant turn rich in content kinds (phased text, thinking
		// + signature, redacted thinking in both encodings, web_search, tool_call)
		// plus the turn-level response handles, then a tool-results turn with an
		// error + duration, and an orphaned tool-results turn. Hits the metadata
		// attach path, convertAssistantTurn's branches, and convertToolResults's
		// error/duration maps — structures the random fuzzer rarely synthesizes.
		`{"header":{"session_id":"s2","model":"gpt-5.5","profile_id":"openai","build_version":"v1.2.3","working_dir":"/w","parent_session_id":"p1"},
"api_calls":[{"kind":"api_call","seq":0,"history_mode":"responses_delta","previous_response_id_hash":"h_prev","conversation_id_hash":"h_conv","request":{"endpoint_family":"responses","request_fingerprint":"fp1","context_marker":"cm1","storage_scope_fingerprint":"ssf1","storage_policy_label":"spl1","previous_response_id_hash":"rh","conversation_id_hash":"rc","history_mode":"full_history_fallback"}}],
"entries":[
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","timestamp":"2026-06-01T10:00:00Z","message":{"role":"user","content":[{"kind":"text","text":"hi"}]}}},
{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","timestamp":"2026-06-01T10:00:01Z","response_id":"resp_1","response_id_hash":"h_prev","response_provider":"openai","response_model":"gpt-5.5","response_request_model":"gpt-5.5","response_endpoint":"responses","response_storage_scope_fingerprint":"ssf","response_request_fingerprint":"rf","response_context_marker":"ctx","message":{"role":"assistant","content":[{"kind":"text","text":"commentary","phase":"commentary"},{"kind":"text","text":"final","phase":"final_answer"},{"kind":"thinking","thinking":{"text":"reason","signature":"sig1"}},{"kind":"thinking","thinking":{"redacted":true}},{"kind":"redacted_thinking"},{"kind":"web_search","web_search":{"query":"golang","raw":{"results":[1,2]}}},{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]}}},
{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","timestamp":"2026-06-01T10:00:02Z","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"out","is_error":true,"duration_ms":42}}]}}},
{"kind":"entry","seq":3,"turn":{"kind":"STEERING","timestamp":"2026-06-01T10:00:03Z","message":{"role":"user","content":[{"kind":"text","text":"steer"}]}}},
{"kind":"entry","seq":4,"turn":{"kind":"SYSTEM","message":{"role":"system","content":[{"kind":"text","text":"sys"}]}}},
{"kind":"entry","seq":5,"turn":{"kind":"CHECKPOINT","message":{"role":"assistant","content":[{"kind":"text","text":"cp"}]}}},
{"kind":"entry","seq":6,"turn":{"kind":"SUMMARY","message":{"role":"assistant","content":[{"kind":"text","text":"sum"}]}}},
{"kind":"entry","seq":7,"turn":{"kind":"TOOL_RESULTS","timestamp":"2026-06-01T10:00:07Z","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"orphan","content":"x","is_error":false}}]}}}
]}`,
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

		// The public wrappers a caller reaches for must also never panic.
		_ = Convert(doc.Header, doc.Entries)
		_ = ConvertWithOptions(doc.Header, doc.Entries, Options{})

		// Convert under BOTH provider-handle modes: raw-local exercises the raw
		// response-id / conversation-id disclosure branches that redacted skips.
		for _, mode := range []ProviderHandleMode{ProviderHandleModeRedacted, ProviderHandleModeRawLocal} {
			traj := ConvertTranscriptWithOptions(doc.Header, doc.Entries, doc.Calls, Options{ProviderHandles: mode})

			if traj.SchemaVersion == "" {
				t.Fatalf("converted trajectory missing schema version")
			}
			if _, err := json.Marshal(traj); err != nil {
				t.Fatalf("trajectory does not re-marshal: %v", err)
			}
		}
	})
}
