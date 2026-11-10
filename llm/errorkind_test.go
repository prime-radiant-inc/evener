package llm_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
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
		// Message text never reclassifies a 429; only a usage-limit error code
		// in the response body does (see TestUsageLimit429IsQuotaExceededAndNotRetryable).
		{429, "quota exceeded", llm.KindRateLimit},
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

// TestProviderFailureSummary pins the sanitized failure summary's behavior:
// it is built from the structured kind and HTTP status, never from
// provider-supplied text, and the sign-in sentinel renders its own arm rather
// than the bare-kind fallback. The exact wording is copy a person reads —
// reviewed in diffs, not pinned here.
func TestProviderFailureSummary(t *testing.T) {
	cases := []struct {
		name string
		err  error
		// leaks is text this test embedded in the error; a sanitized summary
		// must never repeat it.
		leaks []string
		// bareKind: with no HTTP status to render, the documented fallback is
		// the error's structured kind token alone.
		bareKind bool
		// status: an HTTP failure's summary must carry the status code and
		// the kind token.
		status int
		// overridesKind: the sentinel renders its own arm rather than the
		// bare-kind fallback.
		overridesKind bool
	}{
		{"nil error", nil, nil, true, 0, false},
		{"foreign error", errors.New("totally unrelated"), []string{"totally unrelated"}, true, 0, false},
		{"sign-in sentinel", &llm.ConfigurationError{Message: "please sign in again", Cause: llm.ErrSignInRequired}, []string{"please sign in again"}, false, 0, true},
		{"HTTP failure", llm.ErrorFromHTTPStatus("openai", 401, "credential fragment in the body text", nil, nil), []string{"credential fragment in the body text"}, false, 401, false},
		{"non-HTTP failure", llm.NewRequestTimeoutError("openai", "deadline exceeded", nil), []string{"deadline exceeded"}, true, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.ProviderFailureSummary(tc.err)
			kind := llm.Kind(tc.err).String()
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Fatalf("ProviderFailureSummary leaked provider-supplied text %q: %q", leak, got)
				}
			}
			switch {
			case tc.bareKind:
				if got != kind {
					t.Fatalf("ProviderFailureSummary = %q, want the structured kind token %q", got, kind)
				}
			case tc.status != 0:
				if !strings.Contains(got, strconv.Itoa(tc.status)) || !strings.Contains(got, kind) {
					t.Fatalf("ProviderFailureSummary = %q, want the status %d and the kind token %q", got, tc.status, kind)
				}
				if got == kind {
					t.Fatalf("ProviderFailureSummary = %q dropped the HTTP status", got)
				}
			default:
				if got == kind {
					t.Fatalf("ProviderFailureSummary = %q fell back to the kind token; the sign-in sentinel must render its own arm", got)
				}
			}
		})
	}
}
