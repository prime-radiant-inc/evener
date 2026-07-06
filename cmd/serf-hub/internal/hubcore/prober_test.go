package hubcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/rendezvous"
)

type statusStub struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

func TestStatusProber_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(statusStub{SessionID: "01SESS001", State: "idle"})
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()
	p := &StatusProber{Timeout: 500 * time.Millisecond}
	gotSess, gotStatus, _, ok := p.Probe(rendezvous.Entry{Address: addr})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if gotSess != "01SESS001" {
		t.Errorf("session_id: got %q", gotSess)
	}
	if gotStatus != "idle" {
		t.Errorf("state: got %q", gotStatus)
	}
}

func TestStatusProber_NetworkFailure(t *testing.T) {
	p := &StatusProber{Timeout: 100 * time.Millisecond}
	_, _, _, ok := p.Probe(rendezvous.Entry{Address: "127.0.0.1:1"}) // port 1 not listening
	if ok {
		t.Fatal("expected ok=false on closed port")
	}
}

func TestStatusProber_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	p := &StatusProber{Timeout: 100 * time.Millisecond}
	_, _, _, ok := p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String()})
	if ok {
		t.Fatal("expected ok=false on bad JSON")
	}
}

func TestStatusProberSendsHubTokenBearer(t *testing.T) {
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(statusStub{SessionID: "01SESS001", State: "idle"})
	}))
	defer srv.Close()

	p := &StatusProber{Timeout: 500 * time.Millisecond}
	_, _, _, ok := p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String(), HubToken: "secret-token"})
	if !ok {
		t.Fatal("expected ok=true")
	}
	select {
	case auth := <-gotAuth:
		if auth != "Bearer secret-token" {
			t.Fatalf("Authorization=%q, want bearer token", auth)
		}
	default:
		t.Fatal("server did not receive probe")
	}
}

func TestStatusProber_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Valid JSON body so ok=false can only come from the status guard,
		// not a JSON-decode failure (which TestStatusProber_BadJSON covers).
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(statusStub{SessionID: "01SESS001", State: "idle"})
	}))
	defer srv.Close()
	p := &StatusProber{Timeout: 100 * time.Millisecond}
	gotSess, gotStatus, _, ok := p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String()})
	if ok {
		t.Fatal("expected ok=false on non-200 status")
	}
	if gotSess != "" {
		t.Errorf("session_id: got %q, want empty on non-200", gotSess)
	}
	if gotStatus != "" {
		t.Errorf("state: got %q, want empty on non-200", gotStatus)
	}
}

func TestStatusProber_DecodesPendingAsk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"session_id": "01A", "state": "awaiting", "pending_ask": true})
	}))
	defer srv.Close()
	p := &StatusProber{}
	entry := rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")}
	sessID, status, pendingAsk, ok := p.Probe(entry)
	if !ok || sessID != "01A" || status != "awaiting" || !pendingAsk {
		t.Fatalf("Probe() = %q, %q, %v, %v; want 01A, awaiting, true, true", sessID, status, pendingAsk, ok)
	}
}

func TestStatusProber_AbsentPendingAskDecodesFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"session_id": "01A", "state": "active"})
	}))
	defer srv.Close()
	p := &StatusProber{}
	entry := rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")}
	_, _, pendingAsk, _ := p.Probe(entry)
	if pendingAsk {
		t.Fatal("absent pending_ask (old daemon / Codex thread) must decode as false")
	}
}
