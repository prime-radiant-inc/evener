package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type statusStub struct {
	SessionID string `json:"session_id"`
}

func TestStatusProber_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(statusStub{SessionID: "01SESS001"})
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()
	p := &StatusProber{Timeout: 500 * time.Millisecond}
	got, ok := p.Probe(addr)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "01SESS001" {
		t.Errorf("session_id: got %q", got)
	}
}

func TestStatusProber_NetworkFailure(t *testing.T) {
	p := &StatusProber{Timeout: 100 * time.Millisecond}
	_, ok := p.Probe("127.0.0.1:1") // port 1 not listening
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
	_, ok := p.Probe(srv.Listener.Addr().String())
	if ok {
		t.Fatal("expected ok=false on bad JSON")
	}
}
