package google

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzGoogleComplete drives the Gemini non-streaming Complete path against a
// local httptest server replaying the fuzz bytes, status steered by the first
// byte. It exercises the non-streaming response decode (fromGeminiResponse +
// normalizeJSONNumbers on a 2xx body) and the HTTP error mapping
// (ErrorFromHTTPStatusWithRawBodies + classifyGeminiError on a non-2xx body).
//
// Oracles:
//   - never panics for arbitrary bodies (floor);
//   - a non-2xx status always yields a non-nil error;
//   - a 2xx status that decodes without error yields a Response stamped with the
//     google provider.
func FuzzGoogleComplete(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`),
		[]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"shell","args":{"cmd":"ls"}}}]}}]}`),
		[]byte(`{"error":{"code":429,"message":"resource exhausted","status":"RESOURCE_EXHAUSTED"}}`),
		[]byte(`{"candidates":"not-an-array"}`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`not json`),
		[]byte("{\"candidates\":\xff}"),
		[]byte(`[1,2,3]`),
	}
	for _, s := range seeds {
		f.Add(byte(200), s)
	}
	f.Add(byte(29), []byte(`{"error":{"message":"quota"}}`)) // 429
	f.Add(byte(0), []byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`))

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

		a := &Adapter{APIKey: "k", BaseURL: srv.URL}
		resp, err := a.Complete(context.Background(), llm.Request{
			Model:    "gemini-test",
			Messages: []llm.Message{llm.User("hi")},
		})

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("Complete: nil error for HTTP status %d (body %q)", status, body)
			}
			return
		}
		if err != nil {
			return
		}
		if resp.Provider != "google" {
			t.Fatalf("Complete on 2xx: provider = %q, want \"google\" (body %q)", resp.Provider, body)
		}
	})
}
