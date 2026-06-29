package kimi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzCountInputTokensResponse drives the kimi adapter's REAL response-decode
// seam: CountInputTokens issues a request to the estimate-token-count endpoint
// and runs the reply through json.NewDecoder (UseNumber) plus the data extraction
// and HTTP-error mapping in adapter.go. A local httptest server replays the fuzz
// bytes as the response body, with the status code steered by the first byte so
// the search reaches both the success and error-mapping branches.
//
// Oracle: no panic across the decode + IntFromAny + ErrorFromHTTPStatus paths
// for arbitrary (including malformed / non-UTF8) bodies; the helper must never
// return both a nil error and a result it could not have parsed. On the 2xx
// branch the call must always yield a provider-sourced, exact count.
func FuzzCountInputTokensResponse(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"data":{"total_tokens":321}}`),
		[]byte(`{"data":{"total_tokens":"77"}}`),
		[]byte(`{"data":{}}`),
		[]byte(`{"data":null}`),
		[]byte(`{"error":{"message":"slow down"}}`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`not json`),
		[]byte(`{"data":{"total_tokens":1.5}}`),
		[]byte("{\"data\":{\"total_tokens\":\xff}}"),
		[]byte(`[1,2,3]`),
	}
	for _, s := range seeds {
		f.Add(byte(200), s)
	}
	f.Add(byte(0), []byte(`{"error":{"message":"bad"}}`))
	f.Add(byte(99), []byte(`{"error":"invalid_request"}`))

	f.Fuzz(func(t *testing.T, statusSel byte, body []byte) {
		// Map the selector onto a realistic HTTP status so the fuzzer can drive
		// both the 2xx decode branch and the non-2xx error-mapping branch.
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

		a := NewForInstance(InstanceParams{Name: "kimi", BaseURL: srv.URL, APIKey: "k"})
		got, err := a.CountInputTokens(context.Background(), llm.Request{
			Model:    "kimi-k2.6",
			Messages: []llm.Message{llm.User("hello")},
		})

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("CountInputTokens: nil error for HTTP status %d (body %q)", status, body)
			}
			return
		}
		if err != nil {
			return // a malformed 2xx body that fails before producing a count is acceptable.
		}
		if !got.Exact || got.Source != llm.TokenCountSourceProvider {
			t.Fatalf("CountInputTokens on 2xx = %+v, want exact provider count (body %q)", got, body)
		}
	})
}
