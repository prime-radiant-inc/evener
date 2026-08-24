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

func TestSafeMobileOriginRejectsLoopbackAlternateSpellings(t *testing.T) {
	for _, origin := range []string{
		"https://localhost",
		"https://LOCALHOST.",
		"https://LoCaLhOsT.",
		"https://127.1",
		"https://2130706433",
		"https://0x7f000001",
		"https://１２７.０.０.１",
		"https://１２７．１",
		"https://０ｘ７ｆ０００００１",
		"https://²¹³⁰⁷⁰⁶⁴³³",
	} {
		t.Run(origin, func(t *testing.T) {
			if got, ok := safeMobileOrigin(origin); ok {
				t.Fatalf("safeMobileOrigin(%q) = %q, true; want rejection", origin, got)
			}
		})
	}
}

func TestSafeMobileOriginAllowsOrdinaryHTTPSDNSNameWithoutResolution(t *testing.T) {
	for _, origin := range []string{
		"https://unresolvable.example.test:9443",
		"https://bücher.example:9443",
		"https://foo_bar.example:9443",
		"https://-foo.example:9443",
		"https://foo-.example:9443",
	} {
		t.Run(origin, func(t *testing.T) {
			if got, ok := safeMobileOrigin(origin); !ok || got != origin {
				t.Fatalf("safeMobileOrigin(%q) = %q, %v; want unchanged origin", origin, got, ok)
			}
		})
	}
}

func TestSafeMobileOriginRejectsInvalidIDNAHostname(t *testing.T) {
	const origin = "https://a\u200db.example"
	if got, ok := safeMobileOrigin(origin); ok {
		t.Fatalf("safeMobileOrigin(%q) = %q, true; want rejection", origin, got)
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
