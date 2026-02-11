package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRateLimitHeaders_AllPresent(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "99")
	h.Set("x-ratelimit-limit-requests", "100")
	h.Set("x-ratelimit-remaining-tokens", "9999")
	h.Set("x-ratelimit-limit-tokens", "10000")
	h.Set("x-ratelimit-reset-requests", "2026-02-10T12:00:00Z")

	info := ParseRateLimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil RateLimitInfo")
	}
	if info.RequestsRemaining == nil || *info.RequestsRemaining != 99 {
		t.Fatalf("RequestsRemaining = %v, want 99", info.RequestsRemaining)
	}
	if info.RequestsLimit == nil || *info.RequestsLimit != 100 {
		t.Fatalf("RequestsLimit = %v, want 100", info.RequestsLimit)
	}
	if info.TokensRemaining == nil || *info.TokensRemaining != 9999 {
		t.Fatalf("TokensRemaining = %v, want 9999", info.TokensRemaining)
	}
	if info.TokensLimit == nil || *info.TokensLimit != 10000 {
		t.Fatalf("TokensLimit = %v, want 10000", info.TokensLimit)
	}
	wantReset := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	if info.ResetAt == nil {
		t.Fatal("ResetAt is nil")
	}
	if !info.ResetAt.Equal(wantReset) {
		t.Fatalf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
}

func TestParseRateLimitHeaders_NoHeaders_ReturnsNil(t *testing.T) {
	h := http.Header{}
	info := ParseRateLimitHeaders(h)
	if info != nil {
		t.Fatalf("expected nil, got %+v", info)
	}
}

func TestParseRateLimitHeaders_PartialHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "50")

	info := ParseRateLimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil RateLimitInfo")
	}
	if info.RequestsRemaining == nil || *info.RequestsRemaining != 50 {
		t.Fatalf("RequestsRemaining = %v, want 50", info.RequestsRemaining)
	}
	if info.RequestsLimit != nil {
		t.Fatalf("RequestsLimit should be nil, got %v", info.RequestsLimit)
	}
}

func TestParseRateLimitHeaders_InvalidNumber_Ignored(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "not-a-number")
	h.Set("x-ratelimit-limit-tokens", "1000")

	info := ParseRateLimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil (limit-tokens is valid)")
	}
	if info.RequestsRemaining != nil {
		t.Fatalf("RequestsRemaining should be nil for invalid number, got %v", info.RequestsRemaining)
	}
	if info.TokensLimit == nil || *info.TokensLimit != 1000 {
		t.Fatalf("TokensLimit = %v, want 1000", info.TokensLimit)
	}
}

func TestParseRateLimitHeaders_ResetTokensFallback(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-reset-tokens", "2026-02-10T13:00:00Z")

	info := ParseRateLimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil RateLimitInfo")
	}
	wantReset := time.Date(2026, 2, 10, 13, 0, 0, 0, time.UTC)
	if info.ResetAt == nil {
		t.Fatal("ResetAt is nil")
	}
	if !info.ResetAt.Equal(wantReset) {
		t.Fatalf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
}

func TestParseRateLimitHeaders_ResetAt_HTTPDate(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-reset-requests", "Tue, 10 Feb 2026 14:00:00 GMT")

	info := ParseRateLimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil RateLimitInfo")
	}
	wantReset := time.Date(2026, 2, 10, 14, 0, 0, 0, time.UTC)
	if info.ResetAt == nil {
		t.Fatal("ResetAt is nil for HTTP-date format")
	}
	if !info.ResetAt.Equal(wantReset) {
		t.Fatalf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
}

func TestParseRateLimitHeaders_ResetAt_InvalidTimeIgnored(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-reset-requests", "not-a-time")
	h.Set("x-ratelimit-remaining-requests", "5")

	info := ParseRateLimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil (remaining-requests is valid)")
	}
	if info.ResetAt != nil {
		t.Fatalf("ResetAt should be nil for invalid time, got %v", info.ResetAt)
	}
}
