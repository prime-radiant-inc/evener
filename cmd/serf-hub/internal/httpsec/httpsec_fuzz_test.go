package httpsec

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func FuzzCSPMiddleware(f *testing.F) {
	for _, seed := range []struct{ method, target string }{{"GET", "/"}, {"POST", "/api"}, {"OPTIONS", "/x?q=1"}} {
		f.Add(seed.method, seed.target)
	}
	f.Fuzz(func(t *testing.T, method, target string) {
		if len(method) > 32 || len(target) > 4096 {
			t.Skip()
		}
		req, err := http.NewRequest(method, "http://example.test"+target, nil)
		if err != nil {
			return
		}
		called := false
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
		rec := httptest.NewRecorder()
		CSPMiddleware(next).ServeHTTP(rec, req)
		if !called || rec.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("middleware did not preserve contract")
		}
	})
}
