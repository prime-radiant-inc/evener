package httpsec

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
		// style-src allows fonts.googleapis.com so the Google Fonts CSS
		// (loaded by app.html for Hanken Grotesk + JetBrains Mono) can be
		// fetched. 'unsafe-inline' remains because settings partials and
		// app.html use inline <style>/style attributes for data-driven
		// values (e.g., context-bar fill width).
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		// font-src allows fonts.gstatic.com so the WOFF2 font files
		// referenced by the Google Fonts CSS can be downloaded.
		"font-src 'self' https://fonts.gstatic.com",
		// img-src must include data: (transcript-inline base64 thumbnails),
		// blob: (composer-attachments reencodeToPng pipeline; kata 1pgw), and
		// https: (URL-backed AppWire replay images).
		"img-src 'self' data: blob: https:",
		"frame-ancestors 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got: %s", want, csp)
		}
	}
}
