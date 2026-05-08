package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/rendezvous"
)

func TestWeb_Landing_Renders(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	idx := NewPastIndex("")
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    idx,
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "live sessions") {
		t.Errorf("body missing 'live sessions': %q", rec.Body.String())
	}
}

func TestWeb_Assets_ServeHtmx(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/assets/htmx.min.js", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.Len() < 1000 {
		t.Errorf("htmx.min.js too small: %d bytes", rec.Body.Len())
	}
}

func TestWeb_LiveRoster_Partial(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/live", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no live daemons") {
		t.Errorf("expected empty roster message")
	}
}

func TestWeb_PastSearch(t *testing.T) {
	root := t.TempDir()
	proj := root + "/projects/x"
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveSessionMeta(proj, agent.SessionMeta{
		ID: "01A", UpdatedAt: time.Now(), OriginalTask: "fix the bug",
	}); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndex(root + "/projects/*")
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    idx,
	})
	req := httptest.NewRequest(http.MethodGet, "/past?q=bug", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "01A") {
		t.Errorf("expected to find 01A in body, got: %q", rec.Body.String())
	}
}

func TestWeb_DrivePage_KnownSession(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1, Address: "127.0.0.1:55555"})
	r := NewRoster(dir, fakeProber{sessionID: "01SESS001"})
	r.refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/live/01SESS001", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "transcript") {
		t.Errorf("body missing transcript: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "01SESS001") {
		t.Errorf("body missing session id")
	}
}

func TestWeb_DrivePage_UnknownSession_404(t *testing.T) {
	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  NewRoster(t.TempDir(), nil),
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/live/bogus", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d, want 404", rec.Code)
	}
}

func TestWeb_Assets_ServeRenderer(t *testing.T) {
	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/assets/renderer.js", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "SerfRenderer") {
		t.Errorf("renderer.js does not export SerfRenderer")
	}
}
