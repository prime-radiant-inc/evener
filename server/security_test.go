package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerHubTokenRejectsMissingBearer(t *testing.T) {
	srv := NewServer(ServerConfig{HubToken: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestServerHubTokenAllowsMatchingBearer(t *testing.T) {
	srv := NewServer(ServerConfig{HubToken: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestServerSameOriginGuardRejectsBadHost(t *testing.T) {
	srv := NewServer(ServerConfig{AllowedHost: "127.0.0.1:9131"})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Host = "evil.example.com:9131"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "forbidden host") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestServerSameOriginGuardAllowsLocalhostAlias(t *testing.T) {
	srv := NewServer(ServerConfig{AllowedHost: "127.0.0.1:9131"})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Host = "localhost:9131"
	req.Header.Set("Origin", "http://localhost:9131")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestServerSameOriginGuardRejectsBadOrigin(t *testing.T) {
	srv := NewServer(ServerConfig{AllowedHost: "127.0.0.1:9131"})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Host = "127.0.0.1:9131"
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "forbidden origin") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}
