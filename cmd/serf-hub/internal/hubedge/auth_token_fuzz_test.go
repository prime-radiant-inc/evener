package hubedge

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func FuzzAuthGuard(f *testing.F) {
	for _, seed := range []struct{ method, path, cookie, bearer, accept string }{
		{"GET", "/", "secret", "", "text/html"},
		{"POST", "/api/run", "", "secret", "application/json"},
		{"GET", "/api/health", "", "", ""},
		{"GET", "/x?token=secret", "", "", "text/html"},
	} {
		f.Add(seed.method, seed.path, seed.cookie, seed.bearer, seed.accept)
	}
	h := AuthGuard("secret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	f.Fuzz(func(t *testing.T, method, path, cookie, bearer, accept string) {
		if len(method) > 32 || len(path) > 4096 || strings.IndexByte(path, 0) >= 0 {
			t.Skip()
		}
		req, err := http.NewRequest(method, "http://hub.test"+path, nil)
		if err != nil {
			return
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: cookie})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("Accept", accept)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent && rec.Code != http.StatusFound && rec.Code != http.StatusUnauthorized {
			t.Fatalf("unexpected status %d", rec.Code)
		}
	})
}

func FuzzHandleAuth(f *testing.F) {
	for _, next := range []string{"", " ", "/", "//0", "/settings?q=1", "https://evil.test/"} {
		f.Add(next)
	}
	f.Fuzz(func(t *testing.T, next string) {
		if len(next) > 4096 {
			t.Skip()
		}
		req := httptest.NewRequest(http.MethodGet, "/auth?token=secret&next="+url.QueryEscape(next), nil)
		rec := httptest.NewRecorder()
		HandleAuth("secret").ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") {
			t.Fatalf("unsafe redirect %q", loc)
		}
	})
}
