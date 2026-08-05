package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// The PWA manifest must carry the auth token in its start_url so that a
// home-screen launch — which on iOS gets its own cookie jar, separate from the
// browser that authorized the hub — authenticates itself instead of landing on
// the 401 auth wall. The manifest endpoint is auth-gated, so only an already
// authorized browser can read that token.
func TestWeb_Manifest_CarriesAuthTokenInStartURL(t *testing.T) {
	const token = "test-token-abc123"
	web := NewWebServer(hubcore.WebConfig{AuthToken: token, Past: hubcore.NewPastIndex("")})

	// Unauthorized fetch is rejected — the token must not leak to anonymous clients.
	anon := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
	anonRec := httptest.NewRecorder()
	web.Handler().ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous manifest fetch: status %d, want 401", anonRec.Code)
	}

	// Authorized fetch (bearer, as a browser would send the cookie) returns the
	// manifest with the token baked into start_url.
	req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status: %d, body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/manifest+json") {
		t.Errorf("manifest content-type: %q, want application/manifest+json", ct)
	}

	var m struct {
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
		Scope    string `json:"scope"`
		Display  string `json:"display"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\nbody=%q", err, rec.Body.String())
	}
	if m.Name == "" || m.Scope != "/" || m.Display != "standalone" {
		t.Errorf("manifest lost its base fields: %+v", m)
	}
	if !strings.HasPrefix(m.StartURL, "/auth?") {
		t.Fatalf("start_url should self-authenticate via /auth, got %q", m.StartURL)
	}
	if !strings.Contains(m.StartURL, "token="+token) {
		t.Errorf("start_url missing the auth token: %q", m.StartURL)
	}
	if !strings.Contains(m.StartURL, "next=") {
		t.Errorf("start_url should redirect into the app after auth: %q", m.StartURL)
	}
}

// With the guard disabled (empty token, e.g. tests / loopback dev), the
// manifest still serves and just points start_url at the app root.
func TestWeb_Manifest_NoTokenServesPlainStartURL(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status: %d", rec.Code)
	}
	var m struct {
		StartURL string `json:"start_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if m.StartURL != "/" {
		t.Errorf("start_url without a token should be \"/\", got %q", m.StartURL)
	}
}
