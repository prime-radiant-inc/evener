package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzOpenAICompatComplete drives the non-streaming Complete path (Chat
// Completions) against a local httptest server replaying the fuzz bytes, status
// steered by the first byte. It exercises the non-streaming response decode
// (doHTTP JSON parse + fromChatCompletionResponse on a 2xx body) and the HTTP
// error mapping (extractErrorMessage + ErrorFromHTTPStatusWithRawBodies on a
// non-2xx body). Adaptive is left false so the Chat Completions path is taken.
//
// Oracles:
//   - never panics for arbitrary bodies (floor);
//   - a non-2xx status always yields a non-nil error;
//   - a 2xx status that decodes without error yields a Response stamped with the
//     openai-compatible provider.
func FuzzOpenAICompatComplete(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"id":"c1","model":"m","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`),
		[]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"t1","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`),
		[]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`),
		[]byte(`{"choices":[]}`),
		[]byte(`{"choices":"nope"}`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`not json`),
		[]byte("{\"choices\":\xff}"),
		[]byte(`[1,2,3]`),
	}
	for _, s := range seeds {
		f.Add(byte(200), s)
	}
	f.Add(byte(29), []byte(`{"error":{"message":"slow"}}`)) // 429
	f.Add(byte(0), []byte(`garbage but 200`))

	f.Fuzz(func(t *testing.T, statusSel byte, body []byte) {
		status := http.StatusOK
		if statusSel != 0 {
			status = 200 + int(statusSel)%300
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)

		a := NewForInstance(OpenAICompatInstanceParams{Name: "compat", BaseURL: srv.URL, APIKey: "k"})
		resp, err := a.Complete(context.Background(), llm.Request{
			Model:    "m",
			Messages: []llm.Message{llm.User("hi")},
		})

		// completeViaChatCompletions only treats HTTP 200 as success; any other
		// status flows into the error-mapping branch.
		if status != http.StatusOK {
			if err == nil {
				t.Fatalf("Complete: nil error for HTTP status %d (body %q)", status, body)
			}
			return
		}
		if err != nil {
			return // a malformed 2xx body that fails to decode is acceptable.
		}
		if resp.Provider != "openai-compatible" {
			t.Fatalf("Complete on 200: provider = %q, want \"openai-compatible\" (body %q)", resp.Provider, body)
		}
	})
}
