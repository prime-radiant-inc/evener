package llm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/serf/llm"
)

// TestKind_FromHTTPStatus pins the category each constructed error reports, and
// that wrapping is transparent. It is the contract the unexported concrete
// types now sit behind.
func TestKind_FromHTTPStatus(t *testing.T) {
	cases := []struct {
		status  int
		message string
		want    llm.ErrorKind
	}{
		{400, "", llm.KindInvalidRequest},
		{422, "", llm.KindInvalidRequest},
		{401, "", llm.KindAuthentication},
		{403, "", llm.KindAccessDenied},
		{404, "", llm.KindNotFound},
		{408, "", llm.KindTimeout},
		{413, "", llm.KindContextLength},
		{429, "", llm.KindRateLimit},
		{500, "", llm.KindServer},
		{503, "", llm.KindServer},
		{418, "", llm.KindUnknown}, // unmapped status -> unknown HTTP -> KindUnknown
		{400, "content filter triggered", llm.KindContentFilter},
		{400, "context length exceeded", llm.KindContextLength},
		{400, "quota exceeded", llm.KindQuotaExceeded},
		{429, "quota exceeded", llm.KindRateLimit}, // 429 is always rate-limit
	}
	for _, tc := range cases {
		err := llm.ErrorFromHTTPStatus("openai", tc.status, tc.message, nil, nil)
		if got := llm.Kind(err); got != tc.want {
			t.Errorf("Kind(status=%d msg=%q) = %v, want %v", tc.status, tc.message, got, tc.want)
		}
		// Wrapping must be transparent (consumers wrap with %w).
		if got := llm.Kind(fmt.Errorf("provider error: %w", err)); got != tc.want {
			t.Errorf("Kind(wrapped status=%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestKind_Timeout_NonHTTP verifies a non-HTTP context deadline classifies as a
// timeout — the case StatusCode() cannot express (it is 0 here).
func TestKind_Timeout_NonHTTP(t *testing.T) {
	err := llm.NewRequestTimeoutError("openai", "deadline exceeded", context.DeadlineExceeded)
	if got := llm.Kind(err); got != llm.KindTimeout {
		t.Errorf("Kind(non-HTTP timeout) = %v, want %v", got, llm.KindTimeout)
	}
}

// TestKind_NilAndForeign verifies the safe defaults.
func TestKind_NilAndForeign(t *testing.T) {
	if got := llm.Kind(nil); got != llm.KindUnknown {
		t.Errorf("Kind(nil) = %v, want %v", got, llm.KindUnknown)
	}
	if got := llm.Kind(errors.New("plain")); got != llm.KindUnknown {
		t.Errorf("Kind(plain) = %v, want %v", got, llm.KindUnknown)
	}
}
