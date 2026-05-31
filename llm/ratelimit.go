package llm

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ParseRateLimitHeaders extracts rate limit info from standard x-ratelimit-*
// response headers. Returns nil if none of the recognized headers are present
// and parseable.
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
		if t := parseResetTime(v); t != nil {
			info.ResetAt = t
			found = true
		}
	} else if v := strings.TrimSpace(h.Get("x-ratelimit-reset-tokens")); v != "" {
		if t := parseResetTime(v); t != nil {
			info.ResetAt = t
			found = true
		}
	}

	if !found {
		return nil
	}
	return &info
}

// parseResetTime attempts to parse a time string as RFC3339 or HTTP-date.
// Returns nil if neither format matches.
func parseResetTime(v string) *time.Time {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t
	}
	if t, err := http.ParseTime(v); err == nil {
		return &t
	}
	return nil
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
