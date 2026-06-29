package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// FuzzErrorFromHTTPStatus drives the shared HTTP-status → classified-error seam
// used by every provider adapter's non-2xx path (ErrorFromHTTPStatusWithRawBodies
// → errorFromHTTPStatus → extractErrorCode / classifyByMessage). The fuzzer
// supplies the status code and the parsed error body (decoded UseNumber like the
// adapters do), plus a message derived from the body.
//
// Oracles:
//   - never panics for any status / raw shape (floor);
//   - always returns a non-nil, classified error;
//   - the returned error always exposes the supplied status code and provider via
//     the StatusCode()/Provider() accessors the retry/classification layer uses.
func FuzzErrorFromHTTPStatus(f *testing.F) {
	seeds := []struct {
		status int
		body   string
	}{
		{400, `{"error":{"message":"context length exceeded","code":"context_length_exceeded"}}`},
		{401, `{"error":{"type":"authentication_error"}}`},
		{403, `{"error":{"code":"cyber_policy_violation"}}`},
		{404, `{"error":{"message":"model does not exist"}}`},
		{422, `{"error":{"message":"usage policy violation"}}`},
		{429, `{"error":{"message":"slow down"}}`},
		{500, `{}`},
		{503, `not json`},
		{418, `{"error":"teapot"}`},
		{200, `{"error":{"message":"unexpected"}}`},
		{0, ``},
		{-7, `{"error":{"code":42}}`},
	}
	for _, s := range seeds {
		f.Add(s.status, []byte(s.body))
	}

	f.Fuzz(func(t *testing.T, status int, body []byte) {
		var raw map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		_ = dec.Decode(&raw) // adapters tolerate decode failure; raw may be nil.

		ra := ParseRetryAfter("", time.Now())
		err := ErrorFromHTTPStatusWithRawBodies("fuzzprov", status, string(body), raw, ra, "req", string(body))
		if err == nil {
			t.Fatalf("ErrorFromHTTPStatusWithRawBodies returned nil for status %d (body %q)", status, body)
		}

		var sc interface{ StatusCode() int }
		if !errors.As(err, &sc) {
			t.Fatalf("classified error does not expose StatusCode() for status %d: %T", status, err)
		}
		if sc.StatusCode() != status {
			t.Fatalf("classified error StatusCode() = %d, want %d", sc.StatusCode(), status)
		}
		var pr interface{ Provider() string }
		if !errors.As(err, &pr) {
			t.Fatalf("classified error does not expose Provider() for status %d: %T", status, err)
		}
		if pr.Provider() != "fuzzprov" {
			t.Fatalf("classified error Provider() = %q, want \"fuzzprov\"", pr.Provider())
		}
	})
}
