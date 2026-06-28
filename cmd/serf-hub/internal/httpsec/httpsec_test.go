package httpsec

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler writes a distinctive body so tests can confirm the middleware
// delegates to the next handler (HTTPSEC-02).
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("handler-reached"))
	})
}

func TestCSPMiddleware_SetsStrictDefault(t *testing.T) {
	guarded := CSPMiddleware(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)

	// HTTPSEC-02: confirm the middleware calls next.ServeHTTP and does not
	// short-circuit the handler chain.
	if body := rec.Body.String(); body != "handler-reached" {
		t.Fatalf("CSPMiddleware short-circuited handler chain; body = %q, want %q",
			body, "handler-reached")
	}

	// HTTPSEC-01: assert the exact CSP string so any accidental removal,
	// addition, or reordering of a directive is caught immediately.
	const wantCSP = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src 'self' https://fonts.gstatic.com; " +
		"img-src 'self' data: blob: https:; " +
		"connect-src 'self'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'self'"
	if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("Content-Security-Policy mismatch\ngot:  %s\nwant: %s", got, wantCSP)
	}
}
