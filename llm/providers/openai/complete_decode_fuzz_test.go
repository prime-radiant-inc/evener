package openai

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzOpenAIResponsesDecode drives the non-streaming Complete decode seam for the
// Responses API. It mirrors Complete's exact decode steps — a UseNumber JSON
// decode of the response body followed by fromResponses — over fuzzed bytes.
// (Complete itself wraps continuation hashing / fallback logic that needs live
// credentials; fromResponses is the pure decode it calls on a 2xx body, and its
// HTTP error mapping is the shared llm.ErrorFromHTTPStatus seam fuzzed at the llm
// package root.)
//
// Oracles:
//   - never panics for arbitrary (incl. malformed / non-UTF8) bodies (floor);
//   - a body that fails to JSON-decode produces no Response (mirrors Complete
//     returning before fromResponses);
//   - a decodable body always yields a Response stamped with the openai provider.
func FuzzOpenAIResponsesDecode(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"id":"resp_1","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`),
		[]byte(`{"output":[{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}]}`),
		[]byte(`{"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}]}`),
		[]byte(`{"output":"not-an-array"}`),
		[]byte(`{"usage":{"input_tokens":3,"output_tokens":1}}`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`not json`),
		[]byte("{\"output\":\xff}"),
		[]byte(`[1,2,3]`),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		var raw map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			return // Complete returns before fromResponses on a decode error.
		}
		r := fromResponses(raw, "gpt-test")
		if r.Provider != "openai" {
			t.Fatalf("fromResponses: provider = %q, want \"openai\" (body %q)", r.Provider, body)
		}
	})
}
