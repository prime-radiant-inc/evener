package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// dockview-core 7.0.2 opens its default same-origin URL /popout.html in
// window.open, waits for that document's `load` event, then appends the
// popped-out group into document.body and clones the opener's stylesheets in
// (popoutWindow.js:82 url, :83 same-origin assert, :128 load, :135 body
// append, :136 addStyles; dom.js:135 addStyles). The hub must serve a minimal
// same-origin HTML document there: a valid page with a <body> to append into,
// and nothing that would boot a second copy of the app.
func TestWeb_PopoutShell_ServesMinimalSameOriginDocument(t *testing.T) {
	s := newTestWebServer(t)
	rr := authedGet(t, s, "/popout.html")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /popout.html: code=%d body=%q", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type=%q, want text/html; charset=utf-8", ct)
	}
	body := rr.Body.String()
	// dockview appends the popped group into document.body on load, so a body
	// element must exist.
	if !strings.Contains(body, "<body") {
		t.Fatalf("shell needs a <body> for dockview to append into: %q", body)
	}
	// The only content the evidence demands: a charset and a title. dockview
	// overwrites the title with the opener's at load time, but the served
	// document still declares its own identity.
	if !strings.Contains(body, `<meta charset="utf-8">`) {
		t.Fatalf("shell must declare charset utf-8: %q", body)
	}
	if !strings.Contains(body, "<title>serf</title>") {
		t.Fatalf("shell must title itself serf: %q", body)
	}
	// It must NOT boot the SPA: dockview clones the opener's stylesheets into
	// the popout, so the shell needs no CSS or JS of its own, and booting a
	// second app instance here is exactly the failure the served shell exists
	// to prevent (a bare SPA fallback would return index.html and do just that).
	if strings.Contains(body, `id="root"`) {
		t.Fatalf("popout shell must not mount the SPA root: %q", body)
	}
	if strings.Contains(body, "webassets") || strings.Contains(body, "<script") {
		t.Fatalf("popout shell must be inert (no app assets or scripts): %q", body)
	}
}

// The shell is served through the normal auth path — it is NOT in
// hubedge.isAuthExempt — because a same-origin window.open carries the
// SameSite=Lax session cookie: an authorized browser loads it, an anonymous
// client is refused.
func TestWeb_PopoutShell_RequiresAuth(t *testing.T) {
	const token = "test-token-popout"
	s := NewWebServer(hubcore.WebConfig{AuthToken: token, Past: hubcore.NewPastIndex("")})

	anon := httptest.NewRequest(http.MethodGet, "/popout.html", nil)
	anonRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous /popout.html: code=%d, want 401", anonRec.Code)
	}

	authed := httptest.NewRequest(http.MethodGet, "/popout.html", nil)
	authed.Header.Set("Authorization", "Bearer "+token)
	authedRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(authedRec, authed)
	if authedRec.Code != http.StatusOK {
		t.Fatalf("authed /popout.html: code=%d body=%q", authedRec.Code, authedRec.Body.String())
	}
}
