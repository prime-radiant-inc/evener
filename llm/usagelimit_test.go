package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// rawBody decodes a provider error body the way the adapters do (UseNumber, so
// resets_at arrives as json.Number rather than float64).
func rawBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var raw map[string]any
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return raw
}

// The exact body ChatGPT-backed Codex returns when a plan's usage limit is
// exhausted. A multi-day wall must not be retried as a transient rate limit.
const chatGPTUsageLimitBody = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_at":1785258150,"eligible_promo":null,"resets_in_seconds":320387}}`

// The exact body captured from session 0341i3MDP7PKYfPqGhqPWO, provider
// instance kimi-anthropic-api, attempt at 2026-08-05T05:48:28Z, HTTP 403.
// error.type is "permission_error" — a generic Anthropic-API error type, not
// one of usageLimitCodes — and there is no resets_at/resets_in_seconds; only
// the message text names the billing-cycle limit.
const kimiBillingCycle403Body = `{"error":{"type":"permission_error","message":"You've reached your usage limit for this billing cycle. Your quota will be refreshed in the next cycle. To continue now, purchase extra usage or upgrade your plan: https://www.kimi.com/code/#pricing"},"type":"error"}`

func TestParseUsageLimit_MessageOnlyFallback(t *testing.T) {
	raw := rawBody(t, kimiBillingCycle403Body)
	limit, ok := parseUsageLimit(raw, time.Unix(1785000000, 0))
	if !ok {
		t.Fatal("parseUsageLimit reported no usage limit, want a message-only match")
	}
	if !limit.resetsAt.IsZero() {
		t.Fatalf("resetsAt = %v, want zero: the body carries no reset fields", limit.resetsAt)
	}
	if !strings.Contains(limit.message, "usage limit for this billing cycle") {
		t.Fatalf("message = %q, want it to contain %q", limit.message, "usage limit for this billing cycle")
	}
}

// Same error.type ("permission_error") but an unrelated message must not
// match — proves the substring match keys on the phrase, not the error type.
func TestParseUsageLimit_PermissionErrorWithoutUsageLimitPhraseDoesNotMatch(t *testing.T) {
	raw := rawBody(t, `{"error":{"type":"permission_error","message":"you do not have access to this resource"}}`)
	if _, ok := parseUsageLimit(raw, time.Unix(1785000000, 0)); ok {
		t.Fatal("parseUsageLimit matched an unrelated permission_error, want no match")
	}
}

func TestUsageLimit429IsQuotaExceededAndNotRetryable(t *testing.T) {
	raw := rawBody(t, chatGPTUsageLimitBody)
	err := ErrorFromHTTPStatus("openai", 429, "responses.create(stream) failed", raw, nil)

	var target *quotaExceededError
	if !errors.As(err, &target) {
		t.Fatalf("got %T, want *quotaExceededError", err)
	}
	if got := Kind(err); got != KindQuotaExceeded {
		t.Errorf("Kind = %v, want KindQuotaExceeded", got)
	}
	var le Error
	if !errors.As(err, &le) {
		t.Fatalf("not an llm.Error: %T", err)
	}
	if le.Retryable() {
		t.Error("Retryable = true, want false: a usage limit that resets in days must not be retried")
	}
	if got := le.StatusCode(); got != 429 {
		t.Errorf("StatusCode = %d, want 429 preserved", got)
	}
}

// Classify is the single authority the retry chain consults. Its status switch
// maps 429 to Retryable, so a non-retryable usage limit must be recognized
// ahead of that switch or the retry budget burns anyway.
func TestClassifyUsageLimitIsPermanent(t *testing.T) {
	raw := rawBody(t, chatGPTUsageLimitBody)
	err := ErrorFromHTTPStatus("openai", 429, "responses.create(stream) failed", raw, nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify = %v, want ErrorClassPermanent", got)
	}
}

func TestUsageLimitDoesNotRetry(t *testing.T) {
	raw := rawBody(t, chatGPTUsageLimitBody)
	quotaErr := ErrorFromHTTPStatus("openai", 429, "responses.create(stream) failed", raw, nil)

	calls := 0
	_, err := Retry(t.Context(), DefaultRetryPolicy(), noSleep, func() float64 { return 0.5 }, func() (int, error) {
		calls++
		return 0, quotaErr
	})
	if err == nil {
		t.Fatal("Retry returned nil error")
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1: a usage limit must fail on the first attempt", calls)
	}
}

// A plain 429 with no usage-limit marker is still a transient rate limit.
func TestPlainRateLimit429StaysRetryable(t *testing.T) {
	raw := rawBody(t, `{"error":{"type":"rate_limit_exceeded","message":"Slow down"}}`)
	err := ErrorFromHTTPStatus("openai", 429, "responses.create(stream) failed", raw, nil)

	var target *rateLimitError
	if !errors.As(err, &target) {
		t.Fatalf("got %T, want *rateLimitError", err)
	}
	if got := Kind(err); got != KindRateLimit {
		t.Errorf("Kind = %v, want KindRateLimit", got)
	}
	if got := Classify(err); got != ErrorClassRetryable {
		t.Errorf("Classify = %v, want ErrorClassRetryable", got)
	}
}

// OpenAI's standard-API billing wall uses error.code instead of error.type.
func TestInsufficientQuota429IsQuotaExceeded(t *testing.T) {
	raw := rawBody(t, `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`)
	err := ErrorFromHTTPStatus("openai", 429, "chat.completions(stream) failed", raw, nil)

	if got := Kind(err); got != KindQuotaExceeded {
		t.Fatalf("Kind = %v, want KindQuotaExceeded", got)
	}
	if got := Classify(err); got != ErrorClassPermanent {
		t.Errorf("Classify = %v, want ErrorClassPermanent", got)
	}
}

func TestUsageLimitResetAt(t *testing.T) {
	raw := rawBody(t, chatGPTUsageLimitBody)
	err := ErrorFromHTTPStatus("openai", 429, "responses.create(stream) failed", raw, nil)

	got, ok := UsageLimitResetAt(err)
	if !ok {
		t.Fatal("UsageLimitResetAt reported no reset time, want the parsed resets_at")
	}
	if want := time.Unix(1785258150, 0); !got.Equal(want) {
		t.Fatalf("UsageLimitResetAt = %v, want %v", got, want)
	}
}

func TestUsageLimitResetAtAbsentForOtherErrors(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("boom"),
		ErrorFromHTTPStatus("openai", 500, "server exploded", nil, nil),
		ErrorFromHTTPStatus("openai", 429, "slow down", nil, nil),
	} {
		if _, ok := UsageLimitResetAt(err); ok {
			t.Errorf("UsageLimitResetAt(%v) reported a reset time, want none", err)
		}
	}
}

// resets_in_seconds is the fallback when resets_at is missing, and is relative
// to the moment the response was received.
func TestUsageLimitFallsBackToResetsInSeconds(t *testing.T) {
	raw := rawBody(t, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600}}`)
	now := time.Unix(1785000000, 0)

	limit, ok := parseUsageLimit(raw, now)
	if !ok {
		t.Fatal("parseUsageLimit reported no usage limit")
	}
	if want := now.Add(time.Hour); !limit.resetsAt.Equal(want) {
		t.Fatalf("resetsAt = %v, want %v", limit.resetsAt, want)
	}
}

// A usage limit with no reset information at all is still a usage limit: the
// classification must not depend on the provider volunteering a reset time.
func TestUsageLimitWithoutResetTime(t *testing.T) {
	raw := rawBody(t, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)
	err := ErrorFromHTTPStatus("openai", 429, "responses.create(stream) failed", raw, nil)

	if got := Kind(err); got != KindQuotaExceeded {
		t.Fatalf("Kind = %v, want KindQuotaExceeded", got)
	}
	if _, ok := UsageLimitResetAt(err); ok {
		t.Error("UsageLimitResetAt reported a reset time, want none")
	}
	if strings.Contains(err.Error(), "resets") {
		t.Errorf("message mentions a reset it does not know: %q", err.Error())
	}
}

// Nonsense reset values must not produce a bogus timestamp.
func TestUsageLimitIgnoresUnusableResetValues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"zero resets_at", `{"error":{"type":"usage_limit_reached","resets_at":0}}`},
		{"negative resets_at", `{"error":{"type":"usage_limit_reached","resets_at":-5}}`},
		{"non-numeric resets_at", `{"error":{"type":"usage_limit_reached","resets_at":"soon"}}`},
		{"negative resets_in_seconds", `{"error":{"type":"usage_limit_reached","resets_in_seconds":-60}}`},
		{"absurd resets_in_seconds", `{"error":{"type":"usage_limit_reached","resets_in_seconds":1e20}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, ok := parseUsageLimit(rawBody(t, tc.body), time.Unix(1785000000, 0))
			if !ok {
				t.Fatal("parseUsageLimit reported no usage limit, want one with no reset time")
			}
			if !limit.resetsAt.IsZero() {
				t.Fatalf("resetsAt = %v, want zero", limit.resetsAt)
			}
		})
	}
}

func TestFormatResetWindow(t *testing.T) {
	now := time.Date(2026, time.July, 25, 0, 6, 0, 0, time.UTC)
	cases := []struct {
		name     string
		resetsAt time.Time
		want     string
	}{
		{"days and hours", now.Add(89*time.Hour + 56*time.Minute), "in 3d 17h"},
		{"hours and minutes", now.Add(2*time.Hour + 15*time.Minute), "in 2h 15m"},
		{"minutes only", now.Add(45 * time.Minute), "in 45m"},
		{"under a minute", now.Add(20 * time.Second), "in under a minute"},
		{"already elapsed", now.Add(-time.Hour), "now"},
		{"whole hours drop the zero minutes", now.Add(3 * time.Hour), "in 3h"},
		{"whole days drop the zero hours", now.Add(48 * time.Hour), "in 2d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatResetWindow(tc.resetsAt, now)
			if !strings.HasPrefix(got, tc.want+" (") {
				t.Fatalf("formatResetWindow = %q, want relative part %q", got, tc.want)
			}
			// The absolute half is what makes the message actionable across a
			// long wait; it is rendered in the reader's own timezone.
			abs := tc.resetsAt.Local().Format(resetTimeLayout)
			if !strings.Contains(got, "("+abs+")") {
				t.Fatalf("formatResetWindow = %q, want absolute time %q", got, abs)
			}
		})
	}
}

func TestUsageLimitMessageCarriesBothTimeForms(t *testing.T) {
	raw := rawBody(t, chatGPTUsageLimitBody)
	err := ErrorFromHTTPStatus("openai", 429, "responses.create(stream) failed", raw, nil)
	msg := err.Error()

	for _, want := range []string{
		"The usage limit has been reached",
		"plan: pro",
		"resets ",
		time.Unix(1785258150, 0).Local().Format(resetTimeLayout),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "map[") {
		t.Errorf("message leaks a Go map dump: %q", msg)
	}
}
