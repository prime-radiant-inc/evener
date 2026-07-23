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

// spaDist is a minimal built-frontend stand-in whose index.html carries the SPA
// mount point, so serveSPAIndex returns 200 with the shell instead of the
// never-built 503.
func spaDist() fs.FS {
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
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

// TestWebServesSPAForPageRoutes exercises the REAL embedded/dev dist tree, so
// it needs a prior `make build-web`; CI always builds first (ci.yml runs make
// build-web before the Go tests). Locally without a build it skips rather than
// failing with a mysterious 503. Every page route serves the SPA shell now that
// the legacy UI is gone.
func TestWebServesSPAForPageRoutes(t *testing.T) {
	s := newTestWebServer(t)

	if _, err := fs.ReadFile(distFS(), "index.html"); err != nil {
		t.Skipf("frontend not built (run `make build-web`): %v", err)
	}

	for _, path := range []string{"/", "/new", "/s/some-ref", "/settings/theme", "/credentials", "/thread/x"} {
		rr := authedGet(t, s, path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), `id="root"`) {
			t.Fatalf("%s: not the SPA shell: %q", path, rr.Body.String())
		}
	}
}

// TestSerfHubWebEnvIsDead pins that SERF_HUB_WEB no longer gates anything: with
// newWebEnabled() removed, every page route serves the SPA shell regardless of
// the env var's value (unset/empty, "new", or garbage).
func TestSerfHubWebEnvIsDead(t *testing.T) {
	s := newTestWebServerWithDist(t, spaDist())
	var bodies []string
	for _, v := range []string{"", "new", "garbage"} {
		t.Setenv("SERF_HUB_WEB", v)
		rr := authedGet(t, s, "/")
		if rr.Code != http.StatusOK {
			t.Fatalf("SERF_HUB_WEB=%q: code=%d, want 200", v, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), `id="root"`) {
			t.Fatalf("SERF_HUB_WEB=%q: not the SPA shell: %q", v, rr.Body.String())
		}
		bodies = append(bodies, rr.Body.String())
	}
	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Fatalf("SERF_HUB_WEB value changed the response: %q vs %q", bodies[i], bodies[0])
		}
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

// TestSessionImageRouteNotSPAShell pins that /s/{ref}/images/{sha} is served by
// the image handler, not the SPA-shell catch-all: the SPA fetches images
// through this exact route, so it must return image bytes or the not-found
// error, never the HTML shell.
func TestSessionImageRouteNotSPAShell(t *testing.T) {
	s := newTestWebServerWithDist(t, spaDist())
	path := "/s/02wMz5Txv8Vo4rqb3QYZuV/images/" + strings.Repeat("a", 64)
	rr := authedGet(t, s, path)
	if strings.Contains(rr.Body.String(), `id="root"`) {
		t.Fatalf("image route served the SPA shell (code=%d body=%q)", rr.Code, rr.Body.String())
	}
}

func TestWebNextWithoutBuildServes503(t *testing.T) {
	// force the no-index case via the test seam below
	s := newTestWebServerWithDist(t, fstest.MapFS{"PLACEHOLDER": &fstest.MapFile{Data: []byte("x")}})
	rr := authedGet(t, s, "/")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503 run-make-build-web page", rr.Code)
	}
}

// TestWebNextUnmatchedPathsServeSPAShell codifies SPA-fallback semantics:
// unmatched paths return 200 with the SPA shell, delegating routing to the
// client-side router.
func TestWebNextUnmatchedPathsServeSPAShell(t *testing.T) {
	s := newTestWebServerWithDist(t, spaDist())

	for _, path := range []string{"/thread/x/y", "/nonexistent"} {
		rr := authedGet(t, s, path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: code=%d, want 200", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), `id="root"`) {
			t.Fatalf("%s: not the SPA shell: %q", path, rr.Body.String())
		}
	}
}
