package llm

import (
	"net/http"
	"strconv"
	"strings"
)

// ParseRateLimitHeaders extracts rate limit info from standard x-ratelimit-*
// response headers. Returns nil if no rate limit headers are present.
func ParseRateLimitHeaders(h http.Header) *RateLimitInfo {
	info := RateLimitInfo{}
	found := false

	if v := parseHeaderInt(h, "x-ratelimit-remaining-requests"); v != nil {
		info.RequestsRemaining = v
		found = true
	}
	if v := parseHeaderInt(h, "x-ratelimit-limit-requests"); v != nil {
		info.RequestsLimit = v
		found = true
	}
	if v := parseHeaderInt(h, "x-ratelimit-remaining-tokens"); v != nil {
		info.TokensRemaining = v
		found = true
	}
	if v := parseHeaderInt(h, "x-ratelimit-limit-tokens"); v != nil {
		info.TokensLimit = v
		found = true
	}
	if v := strings.TrimSpace(h.Get("x-ratelimit-reset-requests")); v != "" {
		info.ResetAt = v
		found = true
	} else if v := strings.TrimSpace(h.Get("x-ratelimit-reset-tokens")); v != "" {
		info.ResetAt = v
		found = true
	}

	if !found {
		return nil
	}
	return &info
}

func parseHeaderInt(h http.Header, key string) *int {
	v := strings.TrimSpace(h.Get(key))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}
