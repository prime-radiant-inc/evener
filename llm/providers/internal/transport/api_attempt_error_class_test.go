package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"primeradiant.com/serf/llm"
)

// recordProviderRejectClass drives one canonical attempt to a provider-reject
// completion carrying the typed error an adapter would build for that status
// and body, and returns the error_class the capture layer recorded.
//
// It mirrors the real adapter shape (openai/chatcompletions.go and its ~13
// siblings): decode the body, hand it to llm.ErrorFromHTTPStatus, and pass the
// resulting typed error through APIAttemptResult.Err.
func recordProviderRejectClass(t *testing.T, groupID string, statusCode int, body string) string {
	t.Helper()
	var raw map[string]any
	if body != "" {
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatalf("fixture body is not valid JSON: %v", err)
		}
	}
	providerErr := llm.ErrorFromHTTPStatus("openai", statusCode, "provider rejected the request", raw, nil)
	return recordProviderRejectErrorClass(t, groupID, statusCode, providerErr)
}

// recordProviderRejectErrorClass completes an attempt whose adapter result is
// an explicit non-2xx status plus err, and returns the recorded error_class.
func recordProviderRejectErrorClass(t *testing.T, groupID string, statusCode int, err error) string {
	t.Helper()
	sink := &responseAssociationSink{}
	attemptCtx := attemptContext(groupID, sink)
	request, reqErr := http.NewRequestWithContext(attemptCtx, http.MethodPost, "https://provider.test/v1", nil)
	if reqErr != nil {
		t.Fatal(reqErr)
	}
	attempt := BeginAPIAttempt(context.Background(), attemptCtx, request, attemptMeta(request, nil))
	attempt.Complete(llm.APIAttemptResult{StatusCode: statusCode, Err: err}, llm.APITimeoutNone, nil, nil)
	return onlyAttempt(t, sink).ErrorClass
}

// usageLimitBody is the shape a ChatGPT-backed plan returns when the plan
// allowance is spent, as opposed to "you are sending requests too quickly".
// llm.ErrorFromHTTPStatus already parses this into a *quotaExceededError; the
// question these tests ask is whether the capture layer records that fact.
const usageLimitBody = `{"error":{"code":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_in_seconds":209400}}`

// ordinaryRateLimitBody is a transient "slow down" 429 -- same status code as
// usageLimitBody, different condition, and the one that must stay retryable.
const ordinaryRateLimitBody = `{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for gpt-4o"}}`

// TestAPIAttemptErrorClassRecordsQuotaExhausted429 pins the recorded class for
// the condition kata bmgz observed: a 429 whose body names a spent allowance
// that resets days out. Recording it as an ordinary rate limit loses the one
// distinction a caller needs -- retry this, or stop for the session.
func TestAPIAttemptErrorClassRecordsQuotaExhausted429(t *testing.T) {
	got := recordProviderRejectClass(t, "ag_quota_429", http.StatusTooManyRequests, usageLimitBody)
	if got != llm.KindQuotaExceeded.String() {
		t.Fatalf("error_class = %q, want %q: a 429 naming a spent allowance is not an ordinary rate limit",
			got, llm.KindQuotaExceeded)
	}
}

// TestAPIAttemptErrorClassRecordsQuotaExhausted403 is the test that pins the
// property the 429 pair alone cannot: that the recorded class is derived from
// the adapter's typed error rather than from the status code.
//
// A 403 carrying a usage-limit body (Kimi's Anthropic-compatible API reports a
// billing-cycle exhaustion this way) is the case where the two disagree --
// llm.ErrorFromHTTPStatus returns *quotaExceededError while the status table
// says access_denied. An implementation that classified by status would record
// access_denied here, which agent/doctor/apilog.go's classifyAPIErrorClass then
// buckets as "permanent" rather than "quota".
func TestAPIAttemptErrorClassRecordsQuotaExhausted403(t *testing.T) {
	got := recordProviderRejectClass(t, "ag_quota_403", http.StatusForbidden, usageLimitBody)
	if got != llm.KindQuotaExceeded.String() {
		t.Fatalf("error_class = %q, want %q: a 403 naming a spent billing cycle is not a bare access denial",
			got, llm.KindQuotaExceeded)
	}
}

// TestAPIAttemptErrorClassKeepsOrdinaryRateLimit guards the other direction:
// an ordinary 429 must not be swept into the quota class, or the retry chain
// would abandon a limit that clears in seconds.
//
// Honest note on this test's power: it does NOT fail if the typed-error branch
// is deleted outright, because the status table and the typed error agree on
// this input. Its mutation is over-classification (see the report), and
// TestAPIAttemptErrorClassRecordsQuotaExhausted403 carries the derivation
// property instead.
func TestAPIAttemptErrorClassKeepsOrdinaryRateLimit(t *testing.T) {
	got := recordProviderRejectClass(t, "ag_rate_429", http.StatusTooManyRequests, ordinaryRateLimitBody)
	if got != llm.KindRateLimit.String() {
		t.Fatalf("error_class = %q, want %q: a transient 429 must stay retryable", got, llm.KindRateLimit)
	}
}

// TestAPIAttemptErrorClassFallsBackToStatusForUntypedError pins the fallback.
// An adapter that hands back an error this package did not construct still
// gets a class from the recorded status, rather than collapsing to unknown.
func TestAPIAttemptErrorClassFallsBackToStatusForUntypedError(t *testing.T) {
	got := recordProviderRejectErrorClass(t, "ag_untyped", http.StatusNotFound, errors.New("some adapter-specific failure"))
	if got != llm.KindNotFound.String() {
		t.Fatalf("error_class = %q, want %q: an untyped error still classifies from its status", got, llm.KindNotFound)
	}
}
