package llm

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// FuzzParseRateLimitHeaders drives ParseRateLimitHeaders (and parseHeaderInt /
// parseResetTime) over an arbitrary set of x-ratelimit-* header values. This is
// the parser every adapter feeds raw provider headers through; only a couple of
// unit cases exercised it.
//
// Oracles:
//   - the result is non-nil iff at least one recognized header parsed (the
//     documented "returns nil if none present and parseable" contract).
//   - every populated integer field round-trips the exact value we set, so a
//     transform that mis-maps the four counters reddens it.
//   - parsing is deterministic for identical headers.
func FuzzParseRateLimitHeaders(f *testing.F) {
	f.Add("100", "1000", "50", "500", "2026-01-01T00:00:00Z")
	f.Add("", "", "", "", "")
	f.Add("notanint", "x", "", "", "garbage")
	f.Add("0", "", "", "", "Wed, 21 Oct 2026 07:28:00 GMT")

	f.Fuzz(func(t *testing.T, remReq, limReq, remTok, limTok, reset string) {
		h := http.Header{}
		setIf(h, "x-ratelimit-remaining-requests", remReq)
		setIf(h, "x-ratelimit-limit-requests", limReq)
		setIf(h, "x-ratelimit-remaining-tokens", remTok)
		setIf(h, "x-ratelimit-limit-tokens", limTok)
		setIf(h, "x-ratelimit-reset-requests", reset)

		info := ParseRateLimitHeaders(h)

		// Compute which integer fields are expected to parse.
		wantRemReq, okRemReq := atoiTrim(remReq)
		wantLimReq, okLimReq := atoiTrim(limReq)
		wantRemTok, okRemTok := atoiTrim(remTok)
		wantLimTok, okLimTok := atoiTrim(limTok)
		wantReset := parseResetTime(remReqResetCandidate(reset))
		anyInt := okRemReq || okLimReq || okRemTok || okLimTok
		anyFound := anyInt || wantReset != nil

		if anyFound && info == nil {
			t.Fatalf("recognized headers parsed but result nil (headers=%v)", h)
		}
		if !anyFound && info != nil {
			t.Fatalf("no header parsed but result non-nil: %+v", info)
		}
		if info == nil {
			return
		}

		assertIntField(t, "RequestsRemaining", info.RequestsRemaining, wantRemReq, okRemReq)
		assertIntField(t, "RequestsLimit", info.RequestsLimit, wantLimReq, okLimReq)
		assertIntField(t, "TokensRemaining", info.TokensRemaining, wantRemTok, okRemTok)
		assertIntField(t, "TokensLimit", info.TokensLimit, wantLimTok, okLimTok)

		if (info.ResetAt != nil) != (wantReset != nil) {
			t.Fatalf("ResetAt presence mismatch: got %v want %v", info.ResetAt, wantReset)
		}

		// Determinism over a fresh equal header set.
		again := ParseRateLimitHeaders(h)
		if (again == nil) != (info == nil) {
			t.Fatalf("nil-ness not deterministic")
		}
	})
}

func setIf(h http.Header, key, val string) {
	if val != "" {
		h.Set(key, val)
	}
}

func atoiTrim(s string) (int, bool) {
	v := strings.TrimSpace(s)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// remReqResetCandidate mirrors the reset-header precedence: the function reads
// x-ratelimit-reset-requests first; here we only set that header, so the reset
// candidate is the raw value (trimmed exactly as the parser trims it).
func remReqResetCandidate(reset string) string { return strings.TrimSpace(reset) }

func assertIntField(t *testing.T, name string, got *int, want int, present bool) {
	t.Helper()
	if present {
		if got == nil || *got != want {
			t.Fatalf("%s: got %v want %d", name, got, want)
		}
	}
}
