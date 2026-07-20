package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// newTestWebServer builds a WebServer the same way the rest of this package's
// tests do: a zero/minimal hubcore.WebConfig (no AuthToken, so the auth guard
// is disabled — see hubedge.AuthGuard).
func newTestWebServer(t *testing.T) *WebServer {
	t.Helper()
	return NewWebServer(hubcore.WebConfig{})
}

// newTestWebServerWithDist builds a WebServer with the distFS() seam
// overridden to the given fs.FS for the life of the test, so tests can drive
// the built-frontend-present and never-built (503) paths without a real
// `make build-web` output on disk.
func newTestWebServerWithDist(t *testing.T, dist fs.FS) *WebServer {
	t.Helper()
	prev := distFS
	distFS = func() fs.FS { return dist }
	t.Cleanup(func() { distFS = prev })
	return newTestWebServer(t)
}

// authedGet issues a GET through the full Handler() — auth guard included —
// and returns the recorded response. No test in this package configures an
// AuthToken, so the guard is disabled the same way it is everywhere else.
func authedGet(t *testing.T, s *WebServer, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestWebNextServesSPAWhenEnabled(t *testing.T) {
	t.Setenv("SERF_HUB_WEB", "new")
	s := newTestWebServer(t)
	for _, path := range []string{"/", "/new", "/s/some-ref", "/settings/theme", "/thread/x"} {
		rr := authedGet(t, s, path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), `id="root"`) {
			t.Fatalf("%s: not the SPA shell: %q", path, rr.Body.String())
		}
	}
}

func TestWebNextLegacyDefaultUnchanged(t *testing.T) {
	s := newTestWebServer(t)
	rr := authedGet(t, s, "/")
	if strings.Contains(rr.Body.String(), `id="root"`) {
		t.Fatal("legacy default must serve the old shell")
	}
}

// TestWebAssetsServedWithImmutableCache pins the StripPrefix/fs.Sub mapping
// against the real Vite output shape (dist/webassets/<hashed>.ext) and the
// far-future Cache-Control header, then checks a path-escape attempt doesn't
// serve content outside the dist root.
func TestWebAssetsServedWithImmutableCache(t *testing.T) {
	dist := fstest.MapFS{
		"PLACEHOLDER":      &fstest.MapFile{Data: []byte("run make build-web\n")},
		"webassets/app.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
	s := newTestWebServerWithDist(t, dist)

	rr := authedGet(t, s, "/webassets/app.js")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /webassets/app.js: code=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != "console.log(1)" {
		t.Fatalf("body=%q, want the embedded asset content", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control=%q", got)
	}

	rr = authedGet(t, s, "/webassets/../PLACEHOLDER")
	if rr.Code == http.StatusOK {
		t.Fatal("path escape must not serve")
	}
}

func TestWebNextWithoutBuildServes503(t *testing.T) {
	t.Setenv("SERF_HUB_WEB", "new")
	// force the no-index case via the test seam below
	s := newTestWebServerWithDist(t, fstest.MapFS{"PLACEHOLDER": &fstest.MapFile{Data: []byte("x")}})
	rr := authedGet(t, s, "/")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503 run-make-build-web page", rr.Code)
	}
}
