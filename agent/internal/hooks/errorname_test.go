package hooks

import (
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestClaudeErrorName pins the StopFailure error vocabulary. Anything not
// provable maps to "unknown": a hook matcher keys on this string, so a
// plausible-looking wrong name is worse than an honest one.
func TestClaudeErrorName(t *testing.T) {
	cases := []struct {
		name string
		err  error
		// kind is what llm.Kind reports for the fixture. Pinning it keeps the
		// suite honest about WHY each name is expected: llm.Kind re-derives the
		// category from the concrete error type, so a fixture that classifies
		// differently would make the name assertion prove nothing.
		kind llm.ErrorKind
		want string
	}{
		{"nil", nil, llm.KindUnknown, "unknown"},
		{"rate limit", llm.ErrorFromHTTPStatus("p", 429, "slow", nil, nil), llm.KindRateLimit, "rate_limit"},
		{"overloaded 529", llm.ErrorFromHTTPStatus("p", 529, "slow", nil, nil), llm.KindUnknown, "overloaded"},
		{"auth", llm.ErrorFromHTTPStatus("p", 401, "bad key", nil, nil), llm.KindAuthentication, "authentication_failed"},
		{"quota", llm.ErrorFromHTTPStatus("p", 400, "quota exceeded", nil, nil), llm.KindQuotaExceeded, "billing_error"},
		{"billing", llm.ErrorFromHTTPStatus("p", 422, "billing problem", nil, nil), llm.KindQuotaExceeded, "billing_error"},
		{"not found", llm.ErrorFromHTTPStatus("p", 404, "no model", nil, nil), llm.KindNotFound, "model_not_found"},
		{"invalid", llm.ErrorFromHTTPStatus("p", 400, "bad", nil, nil), llm.KindInvalidRequest, "invalid_request"},
		{"server", llm.ErrorFromHTTPStatus("p", 500, "boom", nil, nil), llm.KindServer, "server_error"},
		{"access denied without a code", llm.ErrorFromHTTPStatus("p", 403, "forbidden", nil, nil), llm.KindAccessDenied, "unknown"},
		{"access denied oauth org", llm.ErrorFromHTTPStatus("p", 403, "org not allowed", map[string]any{
			"error": map[string]any{"type": "oauth_org_not_allowed"},
		}, nil), llm.KindAccessDenied, "oauth_org_not_allowed"},
		{"context length", llm.ErrorFromHTTPStatus("p", 413, "too long", nil, nil), llm.KindContextLength, "unknown"},
		{"timeout", llm.ErrorFromHTTPStatus("p", 408, "timed out", nil, nil), llm.KindTimeout, "unknown"},
		{"content filter", llm.ErrorFromHTTPStatus("p", 400, "content filter policy violated", nil, nil), llm.KindContentFilter, "unknown"},
		{"unknown", errors.New("weird"), llm.KindUnknown, "unknown"},
		{"wrapped overloaded", &wrappedOverloaded{llm.ErrorFromHTTPStatus("p", 529, "slow", nil, nil)}, llm.KindUnknown, "overloaded"},
	}
	for _, c := range cases {
		if got := llm.Kind(c.err); got != c.kind {
			t.Fatalf("%s: fixture classifies as %v, want %v — fix the fixture, not the mapping", c.name, got, c.kind)
		}
		if got := ClaudeErrorName(c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// wrappedOverloaded buries the provider error behind another error, the shape a
// retry-group verdict takes in production. The mapping must read the provider
// error through the chain rather than report "server_error" for status 529.
type wrappedOverloaded struct{ err error }

func (w *wrappedOverloaded) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrappedOverloaded) Unwrap() error { return w.err }
