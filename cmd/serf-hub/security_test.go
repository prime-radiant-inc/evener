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

func TestCSPMiddleware_SetsStrictDefault(t *testing.T) {
	guarded := CSPMiddleware(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("CSP header not set")
	}
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self' 'unsafe-inline'",
		// img-src must include data: (transcript-inline base64 thumbnails) and
		// blob: (composer-attachments reencodeToPng pipeline; kata 1pgw).
		"img-src 'self' data: blob:",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got: %s", want, csp)
		}
	}
}
