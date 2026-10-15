package diagnostic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/llm"
)

// Dropping typed classification, retry disposition, or identifier bounding must
// fail independently of the warning's authored prose.
func TestSummaryFailureDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status        int
		message, kind string
		retryable     bool
	}{
		{400, "", "invalid_request", false},
		{401, "", "authentication", false},
		{403, "", "access_denied", false},
		{404, "", "not_found", false},
		{408, "", "timeout", true},
		{413, "", "context_length", false},
		{400, "content filter", "content_filter", false},
		{400, "quota exceeded", "quota_exceeded", false},
		{429, "", "rate_limit", true},
		{503, "", "server", true},
		{418, "", "unknown", true},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			err := llm.ErrorFromHTTPStatus("untrusted-provider", tc.status, tc.message, nil, nil)
			got := SummaryAttemptFromError("provider-fixture", "cheap-fixture", "cheap", fmt.Errorf("wrapped: %w", err))
			if got.Provider != "provider-fixture" || got.Model != "cheap-fixture" || got.Route != "cheap" || got.Kind != tc.kind || got.Stage != "provider" || got.Status != tc.status || got.Retryable == nil || *got.Retryable != tc.retryable {
				t.Fatalf("wrong typed attempt: %+v", got)
			}
			message, info := SummaryFailureInfo(SummaryFailure{Terminal: got})
			if info.Source != SourceProvider || info.Title == "" || info.Hint == "" || !strings.Contains(message, "provider-fixture") || !strings.Contains(message, "cheap-fixture") || !strings.Contains(message, tc.kind) || !strings.Contains(message, strconv.Itoa(tc.status)) {
				t.Fatalf("missing user-facing evidence: %q %+v", message, info)
			}
		})
	}
	for _, tc := range []struct {
		name                string
		err                 error
		kind, stage, reason string
		retryable           *bool
	}{
		{"deadline", context.DeadlineExceeded, "timeout", "unknown", "", nil},
		{"wrapped deadline", fmt.Errorf("opaque: %w", context.DeadlineExceeded), "timeout", "unknown", "", nil},
		{"cancelled", context.Canceled, "cancelled", "unknown", "", nil},
		{"wrapped cancellation", fmt.Errorf("opaque: %w", context.Canceled), "cancelled", "unknown", "", nil},
		{"typed timeout", llm.NewRequestTimeoutError("untrusted", "opaque", context.DeadlineExceeded), "timeout", "provider", "", new(true)},
		{"configuration", &llm.ConfigurationError{Message: "opaque"}, "configuration", "pre_dispatch", "", new(false)},
		{"missing credential", &llm.ConfigurationError{Message: "opaque", Cause: llm.ErrNoCredential}, "configuration", "pre_dispatch", "missing_credential", new(false)},
		{"sign in", &llm.ConfigurationError{Message: "opaque", Cause: llm.ErrSignInRequired}, "configuration", "pre_dispatch", "sign_in_required", new(false)},
		{"budget", &llm.ContextBudgetError{Provider: "untrusted", Model: "untrusted", Limit: "opaque"}, "local_budget", "pre_dispatch", "", new(false)},
		{"unknown", errors.New("quota exceeded authentication context length"), "unknown", "unknown", "", nil},
		{"nil", nil, "unknown", "unknown", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SummaryAttemptFromError("p", "m", "session", tc.err)
			if got.Kind != tc.kind || got.Stage != tc.stage || got.Reason != tc.reason || got.Status != 0 || (got.Retryable == nil) != (tc.retryable == nil) || got.Retryable != nil && *got.Retryable != *tc.retryable {
				t.Fatalf("wrong typed attempt: %+v", got)
			}
		})
	}
}

func TestSummaryFailureDiagnosticsBounds(t *testing.T) {
	t.Parallel()
	for _, status := range []int{-1, 99, 600, 1000000} {
		got := SummaryAttemptFromError("p", "m", "cheap", llm.ErrorFromHTTPStatus("p", status, "opaque", nil, nil))
		if got.Status != 0 {
			t.Fatalf("invalid status escaped: %d", got.Status)
		}
	}
	id := "a\n\r\t\x00\x1b\u2028\u2029\xff" + strings.Repeat("界", 300)
	got := SummaryAttemptFromError(id, id, "cheap", errors.New("opaque"))
	for _, value := range []string{got.Provider, got.Model} {
		if len(value) > 256 || !utf8.ValidString(value) || value == "" || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
			t.Fatalf("unsafe identifier: %q", value)
		}
	}
	encoded, err := json.Marshal(got)
	if err != nil || len(encoded) > 1800 {
		t.Fatalf("unbounded encoding: %d, %v", len(encoded), err)
	}
}
