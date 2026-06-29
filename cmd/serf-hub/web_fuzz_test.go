package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// fuzzSessionID is the local past session the handler fuzz seeds so the
// /doc/file route has a real session cwd to resolve against. It is the session
// the shared sandbox seeds (see sandbox_test.go).
const fuzzSessionID = sandboxSessionID

// fuzzReadOnlyRoutes is the allowlist of hub routes the handler fuzz drives.
// Every entry is a GET-only endpoint that, when the hub is built offline (no
// Roster entries, no codex sources, an empty RunDir), neither spawns a session,
// nor mutates state, nor makes a network call. Mutating routes (/api/spawn,
// /api/upgrade, /api/dirs/create, the POST /api/sessions/.../action verbs),
// routes that shell out (/api/git/head), and routes that probe providers over
// the network (/api/models) are deliberately excluded so a fuzzed request can
// never touch the real environment.
var fuzzReadOnlyRoutes = []string{
	"/",                          // 0
	"/new",                       // 1
	"/assets/",                   // 2  file server — path-escape surface
	"/doc/file",                  // 3  custom file resolver — path-escape surface
	"/manifest.webmanifest",      // 4
	"/_partials/sidebar",         // 5
	"/_partials/workspace/empty", // 6
	"/_partials/workspace/spawn", // 7
	"/_partials/s/",              // 8
	"/_partials/settings",        // 9
	"/s/",                        // 10
	"/thread/",                   // 11
	"/api/tree",                  // 12
	"/api/health",                // 13
	"/api/search",                // 14
	"/api/sessions/",             // 15 GET reads a session detail; POST verbs excluded
	"/settings",                  // 16
}

// newFuzzWebServer builds the contained sandbox hub and returns the server and
// the secret bytes the path-escape oracle guards. The read-only handler fuzz
// shares the same construction as the mutating-route sandbox (see newSandbox).
func newFuzzWebServer(f *testing.F) (*WebServer, []byte) {
	f.Helper()
	s := newSandbox(f)
	return s.Web, s.Secret
}

// buildFuzzRequest turns (base, suffix) into a GET request. The suffix reaches
// the handler as a path segment (percent-escaped so an arbitrary fuzzed string
// always yields a parseable URL) except on /doc/file, where it is delivered as
// the ?path query value — the input the custom file resolver must keep contained
// inside the session cwd.
func buildFuzzRequest(base, suffix string) *http.Request {
	target := base + url.PathEscape(suffix)
	if base == "/doc/file" {
		q := url.Values{}
		q.Set("session", fuzzSessionID)
		q.Set("path", suffix)
		target = base + "?" + q.Encode()
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "127.0.0.1:9180"
	// Internal partials and /doc/file require the HX-Request header; harmless on
	// the other routes.
	req.Header.Set("HX-Request", "true")
	return req
}

func truncateForLog(s string) string {
	const maxLen = 400
	if len(s) > maxLen {
		return s[:maxLen] + "…(truncated)"
	}
	return s
}

// FuzzWebHandler fuzzes the hub's http.Handler over its read-only routes and
// asserts three oracles that "no panic" alone would miss:
//
//   - Never panic (the floor): any panic in routing or param handling fails.
//   - Never 5xx: bad input may legitimately yield a 4xx, but a 500 is a defect —
//     the handler should reject malformed input cleanly, not fault.
//   - Never path-escape: no response may contain the secret planted outside the
//     session cwd, however the fuzzed path or query is shaped.
//
// The hub is built offline (see newFuzzWebServer) and the fuzzed surface is
// restricted to GET-only, non-mutating, non-networked routes so a fuzzed request
// cannot spawn an agent, shell out, or touch anything outside the temp dirs.
func FuzzWebHandler(f *testing.F) {
	web, secret := newFuzzWebServer(f)
	handler := web.Handler()

	seeds := []struct {
		route  uint8
		suffix string
	}{
		{0, ""},                   // /
		{1, ""},                   // /new
		{2, "style.css"},          // /assets/style.css
		{3, "../fuzz-secret.txt"}, // /doc/file traversal — must be refused
		{3, "notes.txt"},          // /doc/file legitimate read
		{4, ""},                   // /manifest.webmanifest
		{5, ""},                   // /_partials/sidebar
		{6, ""},                   // /_partials/workspace/empty
		{9, ""},                   // /_partials/settings (defaults to general)
		{12, ""},                  // /api/tree
		{13, ""},                  // /api/health
		{14, ""},                  // /api/search
		{16, ""},                  // /settings
	}
	for _, s := range seeds {
		f.Add(s.route, s.suffix)
	}

	f.Fuzz(func(t *testing.T, routeIdx uint8, suffix string) {
		base := fuzzReadOnlyRoutes[int(routeIdx)%len(fuzzReadOnlyRoutes)]
		req := buildFuzzRequest(base, suffix)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req) // Oracle 1: never panics.

		// Oracle 2: handlers must never 5xx — bad input may yield a 4xx, but a 500
		// is a defect. /assets/ is included: validAssetPath maps the fs.ErrInvalid
		// path that a bare http.FileServer would 500 on (e.g. /assets/%99) to a
		// 404, so the file server is now held to the same never-5xx contract.
		if rec.Code >= 500 {
			t.Fatalf("5xx from GET %s: status=%d body=%s", req.URL, rec.Code, truncateForLog(rec.Body.String()))
		}
		// Oracle 3: never serve the out-of-root secret.
		if bytes.Contains(rec.Body.Bytes(), secret) {
			t.Fatalf("path escape: GET %s served the out-of-root secret (status=%d)", req.URL, rec.Code)
		}
	})
}
