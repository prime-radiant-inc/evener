package doctor

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// TestClassifyAPIErrorClass_AllBranches covers every branch of
// classifyAPIErrorClass: quota errorClass, caller-cancel outcome, permanent
// and retryable status codes, permanent and retryable error classes, and the
// default fallback.
func TestClassifyAPIErrorClass_AllBranches(t *testing.T) {
	sc400 := 400
	sc429 := 429
	sc500 := 500
	sc200 := 200 // unknown status code

	tests := []struct {
		name       string
		outcome    apilog.AttemptOutcomeClass
		statusCode *int
		errorClass string
		want       string
	}{
		// Quota error class takes priority over everything.
		{"quota class", apilog.AttemptProviderReject, &sc429, llm.KindQuotaExceeded.String(), apiErrorClassQuota},
		// Caller cancel → permanent.
		{"caller cancel", apilog.AttemptCallerCancel, nil, "", apiErrorClassPermanent},
		// Permanent status codes.
		{"400 permanent", apilog.AttemptProviderReject, &sc400, "", apiErrorClassPermanent},
		{"429 retryable by status", apilog.AttemptProviderReject, &sc429, "", apiErrorClassRetryable},
		{"500 retryable by status", apilog.AttemptProviderReject, &sc500, "", apiErrorClassRetryable},
		// Permanent error classes (no status code).
		{"invalid_request class", apilog.AttemptProviderReject, nil, llm.KindInvalidRequest.String(), apiErrorClassPermanent},
		{"authentication class", apilog.AttemptProviderReject, nil, llm.KindAuthentication.String(), apiErrorClassPermanent},
		{"access_denied class", apilog.AttemptProviderReject, nil, llm.KindAccessDenied.String(), apiErrorClassPermanent},
		{"not_found class", apilog.AttemptProviderReject, nil, llm.KindNotFound.String(), apiErrorClassPermanent},
		{"context_length class", apilog.AttemptProviderReject, nil, llm.KindContextLength.String(), apiErrorClassPermanent},
		{"content_filter class", apilog.AttemptProviderReject, nil, llm.KindContentFilter.String(), apiErrorClassPermanent},
		// Retryable error classes (no status code).
		{"timeout class", apilog.AttemptProviderReject, nil, llm.KindTimeout.String(), apiErrorClassRetryable},
		{"rate_limit class", apilog.AttemptProviderReject, nil, llm.KindRateLimit.String(), apiErrorClassRetryable},
		{"server class", apilog.AttemptProviderReject, nil, llm.KindServer.String(), apiErrorClassRetryable},
		// Unknown status code and unknown error class → fallback retryable.
		{"unknown fallback", apilog.AttemptProviderReject, &sc200, "unknown_class", apiErrorClassRetryable},
		// No status code, no error class → fallback retryable.
		{"empty fallback", apilog.AttemptProviderReject, nil, "", apiErrorClassRetryable},
	}
	for _, tc := range tests {
		got := classifyAPIErrorClass(tc.outcome, tc.statusCode, tc.errorClass)
		if got != tc.want {
			t.Errorf("%s: classifyAPIErrorClass(%v, %v, %q) = %q, want %q",
				tc.name, tc.outcome, tc.statusCode, tc.errorClass, got, tc.want)
		}
	}
}

// TestRenderAPIHealth_CoversAllFields covers RenderAPIHealth's output for a
// result with all fields populated.
func TestRenderAPIHealth_CoversAllFields(t *testing.T) {
	r := APIHealthResult{
		SessionID:        "test_sid",
		Attempts:         10,
		RecordedEmpty:    2,
		RetryStormGroups: 1,
		UnsettledGroups:  3,
		ErrorsByClass: map[string]int{
			apiErrorClassQuota:     1,
			apiErrorClassPermanent: 2,
			apiErrorClassRetryable: 4,
		},
		ErrorsByClassQuotaCaveat: "caveat text",
	}
	out := RenderAPIHealth(r)
	for _, want := range []string{
		"session test_sid",
		"attempts=10",
		"recorded_empty=2",
		"retry_storm_groups=1",
		"unsettled_groups=3",
		"quota=1",
		"permanent=2",
		"retryable=4",
		"* caveat text",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderAPIHealth missing %q; got:\n%s", want, out)
		}
	}
}
