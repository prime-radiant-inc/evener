package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzAnthropicComplete drives the Anthropic non-streaming Complete path against
// a local httptest server replaying the fuzz bytes as the response body, with
// the HTTP status steered by the first input byte. This exercises BOTH non-decode
// seams B4 targets: the non-streaming response decode (fromAnthropicResponse on a
// 2xx body) and the HTTP error mapping (ErrorFromHTTPStatusWithRawBodies on a
// non-2xx body).
//
// Oracles:
//   - never panics for arbitrary (incl. malformed / non-UTF8) bodies (floor);
//   - a non-2xx status always yields a non-nil error (structured failure, never
//     a partial success);
//   - a 2xx status that decodes without error always yields a Response stamped
//     with the anthropic provider (no half-built response leaks through).
func FuzzAnthropicComplete(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`),
		[]byte(`{"content":[{"type":"tool_use","id":"t1","name":"shell","input":{"cmd":"ls"}}],"stop_reason":"tool_use"}`),
		[]byte(`{"content":[{"type":"thinking","thinking":"hmm","signature":"s"}]}`),
		[]byte(`{"type":"error","error":{"type":"overloaded_error","message":"slow down"}}`),
		[]byte(`{"content":"not-an-array"}`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`not json`),
		[]byte("{\"content\":\xff}"),
		[]byte(`[1,2,3]`),
	}
	for _, s := range seeds {
		f.Add(byte(200), s)
	}
	f.Add(byte(29), []byte(`{"error":{"message":"bad request"}}`)) // 429
	f.Add(byte(0), []byte(`{"error":{"type":"invalid_request"}}`)) // 200
	f.Add(byte(204), []byte(`garbage`))                            // 204 -> success branch

	f.Fuzz(func(t *testing.T, statusSel byte, body []byte) {
		status := http.StatusOK
		if statusSel != 0 {
			status = 200 + int(statusSel)%300 // 200..499
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)

		a := &Adapter{APIKey: "k", BaseURL: srv.URL}
		resp, err := a.Complete(context.Background(), llm.Request{
			Model:    "claude-test",
			Messages: []llm.Message{llm.User("hi")},
		})

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("Complete: nil error for HTTP status %d (body %q)", status, body)
			}
			return
		}
		if err != nil {
			return // a malformed 2xx body that fails to decode is acceptable.
		}
		if resp.Provider != "anthropic" {
			t.Fatalf("Complete on 2xx: provider = %q, want \"anthropic\" (body %q)", resp.Provider, body)
		}
	})
}
