package providertest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestRegisterStreamingRequiredAuthRegistersOncePerScheme(t *testing.T) {
	for _, tc := range []struct {
		scheme string
		auth   StreamingRequiredAuth
		header string
	}{
		{"providertest-streaming-a", StreamingRequiredAuth{}, ""},
		{"providertest-streaming-b", StreamingRequiredAuth{Authorization: "Bearer b"}, "Bearer b"},
	} {
		RegisterStreamingRequiredAuth(t, tc.scheme, tc.auth)
		RegisterStreamingRequiredAuth(t, tc.scheme, tc.auth)

		auth, ok := llm.AuthenticatorFor(tc.scheme)
		if !ok {
			t.Fatalf("scheme %q was not registered", tc.scheme)
		}
		preparer, ok := auth.(llm.RequestPreparer)
		if !ok || !preparer.RequiresStreamingComplete() {
			t.Fatalf("authenticator for %q does not require streaming completion", tc.scheme)
		}
		req := httptest.NewRequest(http.MethodGet, "https://example.invalid", nil)
		if err := auth.Apply(context.Background(), req, registry.Resolved{}); err != nil {
			t.Fatalf("Apply for %q: %v", tc.scheme, err)
		}
		if got := req.Header.Get("Authorization"); got != tc.header {
			t.Fatalf("Authorization for %q = %q, want %q", tc.scheme, got, tc.header)
		}
	}
}
