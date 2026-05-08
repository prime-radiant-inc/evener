package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/rendezvous"
)

// fakeRoster lets proxy tests resolve session_id -> addr without scanning.
type fakeRoster struct {
	addr string
	id   string
}

func (f fakeRoster) Find(sessionID string) (LiveEntry, bool) {
	if sessionID != f.id {
		return LiveEntry{}, false
	}
	return LiveEntry{
		SessionID: f.id,
		Entry:     rendezvous.Entry{PID: 1, Address: f.addr},
	}, true
}

func TestRESTProxy_RoutesByPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Path", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello-from-daemon")
	}))
	defer upstream.Close()

	resolver := fakeRoster{addr: upstream.Listener.Addr().String(), id: "01SESS001"}
	proxy := NewRESTProxy(resolver)

	req := httptest.NewRequest(http.MethodPost, "/live/01SESS001/input", strings.NewReader(`{"text":"hi"}`))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Upstream-Path"); got != "/input" {
		t.Errorf("upstream path: got %q, want /input", got)
	}
	if body := rec.Body.String(); body != "hello-from-daemon" {
		t.Errorf("body: got %q", body)
	}
}

func TestRESTProxy_404UnknownSession(t *testing.T) {
	resolver := fakeRoster{}
	proxy := NewRESTProxy(resolver)

	req := httptest.NewRequest(http.MethodGet, "/live/missing/status", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}
