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
	addr  string
	id    string
	token string
}

func (f fakeRoster) Find(sessionID string) (LiveEntry, bool) {
	if sessionID != f.id {
		return LiveEntry{}, false
	}
	return LiveEntry{
		SessionID: f.id,
		Entry:     rendezvous.Entry{PID: 1, Address: f.addr, HubToken: f.token},
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

func TestRESTProxyStampsHubTokenBearer(t *testing.T) {
	gotAuth := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	resolver := fakeRoster{addr: upstream.Listener.Addr().String(), id: "01SESS001", token: "secret-token"}
	proxy := NewRESTProxy(resolver)

	req := httptest.NewRequest(http.MethodPost, "/live/01SESS001/input", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Authorization", "Bearer browser-token")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNoContent)
	}
	select {
	case auth := <-gotAuth:
		if auth != "Bearer secret-token" {
			t.Fatalf("Authorization=%q, want bearer token", auth)
		}
	default:
		t.Fatal("upstream did not receive request")
	}
}

func TestRESTProxyStripsBrowserOrigin(t *testing.T) {
	gotOrigin := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrigin <- r.Header.Get("Origin")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	resolver := fakeRoster{addr: upstream.Listener.Addr().String(), id: "01SESS001", token: "secret-token"}
	proxy := NewRESTProxy(resolver)

	req := httptest.NewRequest(http.MethodPost, "/live/01SESS001/input", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNoContent)
	}
	select {
	case origin := <-gotOrigin:
		if origin != "" {
			t.Fatalf("Origin=%q, want stripped", origin)
		}
	default:
		t.Fatal("upstream did not receive request")
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

func TestSSEProxy_PassesThroughEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream needs Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		_, _ = io.WriteString(w, "id: 1\nevent: SESSION_START\ndata: {\"profile\":\"p\"}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "id: 2\nevent: ASSISTANT_TEXT_DELTA\ndata: {\"delta\":\"hi\"}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	resolver := fakeRoster{addr: upstream.Listener.Addr().String(), id: "01SESS001"}
	proxy := NewSSEProxy(resolver)

	req := httptest.NewRequest(http.MethodGet, "/live/01SESS001/events", nil)
	req.Header.Set("Last-Event-ID", "0")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: SESSION_START") {
		t.Errorf("missing SESSION_START in body: %q", body)
	}
	if !strings.Contains(body, "event: ASSISTANT_TEXT_DELTA") {
		t.Errorf("missing ASSISTANT_TEXT_DELTA: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q", ct)
	}
}

func TestSSEProxy_404UnknownSession(t *testing.T) {
	resolver := fakeRoster{}
	proxy := NewSSEProxy(resolver)
	req := httptest.NewRequest(http.MethodGet, "/live/missing/events", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestSSEProxy_ForwardsLastEventID(t *testing.T) {
	gotID := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID <- r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	resolver := fakeRoster{addr: upstream.Listener.Addr().String(), id: "01SESS001"}
	proxy := NewSSEProxy(resolver)

	req := httptest.NewRequest(http.MethodGet, "/live/01SESS001/events", nil)
	req.Header.Set("Last-Event-ID", "42")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	select {
	case id := <-gotID:
		if id != "42" {
			t.Fatalf("Last-Event-ID forwarded as %q, want %q", id, "42")
		}
	default:
		t.Fatal("upstream never received Last-Event-ID header")
	}
}

func TestSSEProxyStripsBrowserOrigin(t *testing.T) {
	gotOrigin := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrigin <- r.Header.Get("Origin")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	resolver := fakeRoster{addr: upstream.Listener.Addr().String(), id: "01SESS001", token: "secret-token"}
	proxy := NewSSEProxy(resolver)

	req := httptest.NewRequest(http.MethodGet, "/live/01SESS001/events", nil)
	req.Header.Set("Origin", "http://127.0.0.1:9180")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}
	select {
	case origin := <-gotOrigin:
		if origin != "" {
			t.Fatalf("Origin=%q, want stripped", origin)
		}
	default:
		t.Fatal("upstream did not receive request")
	}
}

func TestSSEProxyStampsHubTokenBearer(t *testing.T) {
	gotAuth := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	resolver := fakeRoster{addr: upstream.Listener.Addr().String(), id: "01SESS001", token: "secret-token"}
	proxy := NewSSEProxy(resolver)

	req := httptest.NewRequest(http.MethodGet, "/live/01SESS001/events", nil)
	req.Header.Set("Authorization", "Bearer browser-token")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
	}
	select {
	case auth := <-gotAuth:
		if auth != "Bearer secret-token" {
			t.Fatalf("Authorization=%q, want bearer token", auth)
		}
	default:
		t.Fatal("upstream did not receive request")
	}
}
