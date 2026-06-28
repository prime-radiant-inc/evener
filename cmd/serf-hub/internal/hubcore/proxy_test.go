package hubcore

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

func TestSplitLivePath_NonMatching(t *testing.T) {
	_, _, ok := splitLivePath("/other/01A")
	if ok {
		t.Fatal("expected splitLivePath to return false for non-matching path")
	}
}
