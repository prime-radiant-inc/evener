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
	// protocolhttp.Prepare sets the credential headers before Apply runs;
	// mirror that so the tests see what the authenticator actually finds.
	for k, v := range res.CredentialHeaders {
		req.Header.Set(k, v)
	}
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

// TestCredentialHeaderWinsOverDerivedAuth pins the spec §10 rule: when an
// instance supplies the auth header through credential_headers, the header
// wins and the scheme derives nothing from the key, so a "Bearer $KEY"
// header is not turned into "Bearer Bearer $KEY".
func TestCredentialHeaderWinsOverDerivedAuth(t *testing.T) {
	fromHeader := registry.Resolved{
		Instance:          "work",
		Credential:        registry.Credential{Value: "Bearer k-1", Source: "credential_headers"},
		CredentialHeaders: map[string]string{"Authorization": "Bearer k-1"},
	}

	req, err := applyScheme(t, registry.AuthBearer, fromHeader)
	if err != nil || req.Header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("bearer with credential_headers: %v %q", err, req.Header.Get("Authorization"))
	}

	req, err = applyScheme(t, registry.AuthOptionalBearer, fromHeader)
	if err != nil || req.Header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("optional-bearer with credential_headers: %v %q", err, req.Header.Get("Authorization"))
	}

	header := registry.Resolved{
		Instance:          "work",
		Credential:        registry.Credential{Value: "k-1", Source: "api_key"},
		CredentialHeaders: map[string]string{"x-api-key": "header-key"},
	}
	header.Transport.AuthHeader = "X-Api-Key"
	req, err = applyScheme(t, registry.AuthHeader, header)
	if err != nil || req.Header.Get("x-api-key") != "header-key" {
		t.Fatalf("header scheme must not overwrite a credential header: %v %q", err, req.Header.Get("x-api-key"))
	}

	// A credential header carrying the auth header also satisfies the
	// scheme's credential requirement.
	noKey := registry.Resolved{Instance: "work", CredentialHeaders: map[string]string{"X-Api-Key": "header-key"}}
	noKey.Transport.AuthHeader = "X-Api-Key"
	if req, err := applyScheme(t, registry.AuthHeader, noKey); err != nil || req.Header.Get("X-Api-Key") != "header-key" {
		t.Fatalf("header scheme with only a credential header: %v %q", err, req.Header.Get("X-Api-Key"))
	}
}
