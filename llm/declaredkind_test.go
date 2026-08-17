package llm_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"primeradiant.com/serf/llm"
)

// declaredKindStatusCases covers every branch of ErrorFromHTTPStatus that
// yields a distinct concrete error type, including the two body-driven
// reclassifications (429 and 403 usage limits) that the status alone misses.
var declaredKindStatusCases = []struct {
	name    string
	status  int
	message string
	raw     map[string]any
	want    llm.ErrorKind
}{
	{name: "400", status: 400, want: llm.KindInvalidRequest},
	{name: "422", status: 422, want: llm.KindInvalidRequest},
	{name: "401", status: 401, want: llm.KindAuthentication},
	{name: "403", status: 403, want: llm.KindAccessDenied},
	{name: "404", status: 404, want: llm.KindNotFound},
	{name: "408", status: 408, want: llm.KindTimeout},
	{name: "413", status: 413, want: llm.KindContextLength},
	{name: "429", status: 429, want: llm.KindRateLimit},
	{name: "500", status: 500, want: llm.KindServer},
	{name: "503", status: 503, want: llm.KindServer},
	{name: "unmapped status", status: 418, want: llm.KindUnknown},
	{name: "message content filter", status: 400, message: "content filter triggered", want: llm.KindContentFilter},
	{name: "message context length", status: 400, message: "context length exceeded", want: llm.KindContextLength},
	{name: "message quota", status: 400, message: "quota exceeded", want: llm.KindQuotaExceeded},
	{
		name:   "429 usage limit body",
		status: http.StatusTooManyRequests,
		raw:    usageLimitRawBody(),
		want:   llm.KindQuotaExceeded,
	},
	{
		name:   "403 usage limit body",
		status: http.StatusForbidden,
		raw:    usageLimitRawBody(),
		want:   llm.KindQuotaExceeded,
	},
}

func usageLimitRawBody() map[string]any {
	return map[string]any{
		"error": map[string]any{
			"code":    "usage_limit_reached",
			"message": "The usage limit has been reached",
		},
	}
}

// TestDeclaredKindMatchesKindForEveryConstructedError is the net that keeps
// DeclaredKind's per-type methods in sync with Kind's type switch. The two
// mappings are written separately -- Kind walks the chain, DeclaredKind asks
// the value in hand -- so nothing but this test stops them from drifting when a
// new error type is added to the taxonomy.
func TestDeclaredKindMatchesKindForEveryConstructedError(t *testing.T) {
	for _, tc := range declaredKindStatusCases {
		t.Run(tc.name, func(t *testing.T) {
			err := llm.ErrorFromHTTPStatus("openai", tc.status, tc.message, tc.raw, nil)
			if got := llm.Kind(err); got != tc.want {
				t.Fatalf("Kind = %v, want %v (fixture is wrong, fix it before reading the next assertion)", got, tc.want)
			}
			if got := llm.DeclaredKind(err); got != tc.want {
				t.Errorf("DeclaredKind = %v, want %v -- DeclaredKind's method set has drifted from Kind's switch", got, tc.want)
			}
		})
	}
}

// TestDeclaredKindCoversNonHTTPTimeouts pins the constructors that do not go
// through ErrorFromHTTPStatus.
func TestDeclaredKindCoversNonHTTPTimeouts(t *testing.T) {
	err := llm.NewRequestTimeoutError("openai", "deadline exceeded", nil)
	if got := llm.DeclaredKind(err); got != llm.KindTimeout {
		t.Errorf("DeclaredKind(request timeout) = %v, want %v", got, llm.KindTimeout)
	}
}

// TestDeclaredKindStopsAtTheValueInHand pins the one deliberate difference from
// Kind: DeclaredKind does not unwrap. This is not an incidental limitation --
// it is why DeclaredKind can be called on an error of unverified provenance
// without running caller-supplied Unwrap/Is/As code.
func TestDeclaredKindStopsAtTheValueInHand(t *testing.T) {
	inner := llm.ErrorFromHTTPStatus("openai", http.StatusTooManyRequests, "", usageLimitRawBody(), nil)
	wrapped := fmt.Errorf("session namer: %w", inner)

	if got := llm.Kind(wrapped); got != llm.KindQuotaExceeded {
		t.Fatalf("Kind(wrapped) = %v, want %v: Kind must still see through the wrap", got, llm.KindQuotaExceeded)
	}
	if got := llm.DeclaredKind(wrapped); got != llm.KindUnknown {
		t.Errorf("DeclaredKind(wrapped) = %v, want %v: DeclaredKind must not unwrap", got, llm.KindUnknown)
	}
}

// TestDeclaredKindIgnoresForeignErrors pins that a type this package did not
// construct reports unknown rather than being coerced into the taxonomy.
func TestDeclaredKindIgnoresForeignErrors(t *testing.T) {
	if got := llm.DeclaredKind(errors.New("some other failure")); got != llm.KindUnknown {
		t.Errorf("DeclaredKind(foreign) = %v, want %v", got, llm.KindUnknown)
	}
	if got := llm.DeclaredKind(nil); got != llm.KindUnknown {
		t.Errorf("DeclaredKind(nil) = %v, want %v", got, llm.KindUnknown)
	}
}
