package llm

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func applyScheme(t *testing.T, scheme string, res registry.Resolved) (*http.Request, error) {
	t.Helper()
	a, ok := AuthenticatorFor(scheme)
	if !ok {
		t.Fatalf("scheme %q not registered", scheme)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/v1", nil)
	err := a.Apply(context.Background(), req, res)
	return req, err
}

func TestTrivialAuthenticators(t *testing.T) {
	withKey := registry.Resolved{Instance: "groq", Credential: registry.Credential{Value: "k-1", Source: "env:GROQ_API_KEY"}}
	noKey := registry.Resolved{Instance: "groq", Warnings: []string{"no credential (GROQ_API_KEY unset)"}}

	req, err := applyScheme(t, registry.AuthBearer, withKey)
	if err != nil || req.Header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("bearer: %v %q", err, req.Header.Get("Authorization"))
	}
	if _, err := applyScheme(t, registry.AuthBearer, noKey); err == nil || !strings.Contains(err.Error(), "GROQ_API_KEY unset") {
		t.Fatalf("bearer without credential: %v", err)
	}

	req, err = applyScheme(t, registry.AuthOptionalBearer, noKey)
	if err != nil || req.Header.Get("Authorization") != "" {
		t.Fatalf("optional-bearer without credential: %v %q", err, req.Header.Get("Authorization"))
	}
	req, _ = applyScheme(t, registry.AuthOptionalBearer, withKey)
	if req.Header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("optional-bearer with credential: %q", req.Header.Get("Authorization"))
	}

	header := withKey
	header.Transport.AuthHeader = "x-goog-api-key"
	req, err = applyScheme(t, registry.AuthHeader, header)
	if err != nil || req.Header.Get("x-goog-api-key") != "k-1" || req.Header.Get("Authorization") != "" {
		t.Fatalf("header: %v %v", err, req.Header)
	}
	if _, err := applyScheme(t, registry.AuthHeader, withKey); err == nil {
		t.Fatal("header scheme without auth_header must fail")
	}

	req, err = applyScheme(t, registry.AuthNone, noKey)
	if err != nil || len(req.Header) != 0 {
		t.Fatalf("none: %v %v", err, req.Header)
	}
}
