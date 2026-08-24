package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestWeb_MobilePairingRequiresAuth(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mobile/pairing", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestWeb_MobilePairingQueryTokenRedirectIsNoStore(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})
	req := httptest.NewRequest(http.MethodGet, "/api/mobile/pairing?token=mobile-secret", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestWeb_MobilePairingRejectsLoopbackFallback(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9180/api/mobile/pairing", nil)
	req.Header.Set("Authorization", "Bearer mobile-secret")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "mobile-secret") {
		t.Fatalf("unreachable response leaked token: %q", rec.Body.String())
	}
}

func TestWeb_MobilePairingRejectsConfiguredLoopback(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		AuthToken:     "mobile-secret",
		MobileBaseURL: "http://127.0.0.1:9180",
	})
	req := httptest.NewRequest(http.MethodGet, "https://hub.example.test/api/mobile/pairing", nil)
	req.Header.Set("Authorization", "Bearer mobile-secret")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "mobile-secret") {
		t.Fatalf("configured loopback response leaked token: %q", rec.Body.String())
	}
}

func TestWeb_MobilePairingUsesConfiguredBaseURL(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		AuthToken:     "mobile-secret",
		MobileBaseURL: "https://hub.example.test:9443",
	})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9180/api/mobile/pairing", nil)
	req.Header.Set("Authorization", "Bearer mobile-secret")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var response struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.AuthURL != "https://hub.example.test:9443/auth?token=mobile-secret" {
		t.Fatalf("auth_url = %q", response.AuthURL)
	}
}

func TestWeb_MobilePairingRejectsLoopbackAlternateSpellings(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})
	// These alternate loopback spellings bypass netip.ParseAddr but resolve
	// to 127.0.0.1 via the OS resolver.
	for _, origin := range []string{
		"https://localhost.",
		"https://127.1",
		"https://2130706433",
	} {
		req := httptest.NewRequest(http.MethodGet, origin+"/api/mobile/pairing", nil)
		req.Header.Set("Authorization", "Bearer mobile-secret")
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("origin %s: status = %d, want %d: %s", origin, rec.Code, http.StatusConflict, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "mobile-secret") {
			t.Fatalf("origin %s: response leaked token: %q", origin, rec.Body.String())
		}
	}
}

func TestWeb_MobilePairingSucceedsFromRequestOrigin(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})
	// Default path: no MobileBaseURL configured, derive from request origin.
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.20:9180/api/mobile/pairing", nil)
	req.Header.Set("Authorization", "Bearer mobile-secret")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.AuthURL != "http://192.168.1.20:9180/auth?token=mobile-secret" {
		t.Fatalf("auth_url = %q, want http://192.168.1.20:9180/auth?token=mobile-secret", response.AuthURL)
	}
}

func TestWeb_MobilePairingRejectsPublicHTTP(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})
	req := httptest.NewRequest(http.MethodGet, "http://93.184.216.34:9180/api/mobile/pairing", nil)
	req.Header.Set("Authorization", "Bearer mobile-secret")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}
