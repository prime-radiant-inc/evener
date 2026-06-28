package hubcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	gotSess, gotStatus, ok := p.Probe(rendezvous.Entry{Address: addr})
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
	_, _, ok := p.Probe(rendezvous.Entry{Address: "127.0.0.1:1"}) // port 1 not listening
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
	_, _, ok := p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String()})
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
	_, _, ok := p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String(), HubToken: "secret-token"})
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
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	p := &StatusProber{Timeout: 100 * time.Millisecond}
	_, _, ok := p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String()})
	if ok {
		t.Fatal("expected ok=false on non-200 status")
	}
}
