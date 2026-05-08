package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestSameOriginGuard_AllowsLoopbackHostNoOrigin(t *testing.T) {
	guarded := SameOriginGuard("127.0.0.1:9180")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestSameOriginGuard_AllowsLocalhostHost(t *testing.T) {
	guarded := SameOriginGuard("127.0.0.1:9180")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:9180"
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestSameOriginGuard_RejectsNonLoopbackHost(t *testing.T) {
	guarded := SameOriginGuard("127.0.0.1:9180")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.example.com:9180"
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestSameOriginGuard_AllowsCorrectOrigin(t *testing.T) {
	guarded := SameOriginGuard("127.0.0.1:9180")(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestCSPMiddleware_SetsStrictDefault(t *testing.T) {
	guarded := CSPMiddleware(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("CSP header not set")
	}
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got: %s", want, csp)
		}
	}
}

func TestSameOriginGuard_RejectsBadOrigin(t *testing.T) {
	guarded := SameOriginGuard("127.0.0.1:9180")(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}
