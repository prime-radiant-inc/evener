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

func fuzzScenarioStatusProber_Success(t *testing.T) {
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
	got := p.Probe(rendezvous.Entry{Address: addr})
	if !got.OK {
		t.Fatal("expected ok=true")
	}
	if got.SessionID != "01SESS001" {
		t.Errorf("session_id: got %q", got.SessionID)
	}
	if got.Status != "idle" {
		t.Errorf("state: got %q", got.Status)
	}
}

func fuzzScenarioStatusProber_NetworkFailure(t *testing.T) {
	p := &StatusProber{Timeout: 100 * time.Millisecond}
	if p.Probe(rendezvous.Entry{Address: "127.0.0.1:1"}).OK { // port 1 not listening
		t.Fatal("expected ok=false on closed port")
	}
}

func fuzzScenarioStatusProber_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	p := &StatusProber{Timeout: 100 * time.Millisecond}
	if p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String()}).OK {
		t.Fatal("expected ok=false on bad JSON")
	}
}

func fuzzScenarioStatusProberSendsHubTokenBearer(t *testing.T) {
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(statusStub{SessionID: "01SESS001", State: "idle"})
	}))
	defer srv.Close()

	p := &StatusProber{Timeout: 500 * time.Millisecond}
	if !p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String(), HubToken: "secret-token"}).OK {
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

func fuzzScenarioStatusProber_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Valid JSON body so ok=false can only come from the status guard,
		// not a JSON-decode failure (which TestStatusProber_BadJSON covers).
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(statusStub{SessionID: "01SESS001", State: "idle"})
	}))
	defer srv.Close()
	p := &StatusProber{Timeout: 100 * time.Millisecond}
	got := p.Probe(rendezvous.Entry{Address: srv.Listener.Addr().String()})
	if got.OK {
		t.Fatal("expected ok=false on non-200 status")
	}
	if got.SessionID != "" {
		t.Errorf("session_id: got %q, want empty on non-200", got.SessionID)
	}
	if got.Status != "" {
		t.Errorf("state: got %q, want empty on non-200", got.Status)
	}
}

func fuzzScenarioStatusProber_DecodesPendingAsk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"session_id": "01A", "state": "awaiting", "pending_ask": true})
	}))
	defer srv.Close()
	p := &StatusProber{}
	entry := rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")}
	got := p.Probe(entry)
	if !got.OK || got.SessionID != "01A" || got.Status != "awaiting" || !got.PendingAsk {
		t.Fatalf("Probe() = %+v; want 01A, awaiting, pending ask, ok", got)
	}
}

func fuzzScenarioStatusProber_DecodesPendingEscalation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"session_id": "01A", "state": "active", "pending_escalation": true})
	}))
	defer srv.Close()
	p := &StatusProber{}
	entry := rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")}
	got := p.Probe(entry)
	if !got.OK || got.SessionID != "01A" || got.Status != "active" || !got.PendingEscalation {
		t.Fatalf("Probe() pendingEscalation path = %+v; want 01A, active, pending escalation, ok", got)
	}
}

func fuzzScenarioStatusProber_DecodesRunningSubagentIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": "01PARENT",
			"state":      "idle",
			"detailed": map[string]any{"jobs": []map[string]any{
				{"job_type": "delegate", "status": "running", "transcript_ref": "local:child-running"},
				{"job_type": "delegate", "status": "completed", "transcript_ref": "local:child-done"},
				{"job_type": "shell", "status": "running", "transcript_ref": "local:not-a-child"},
				{"job_type": "delegate", "status": "running", "transcript_ref": "codex:remote-child"},
				{"job_type": "delegate", "status": "running", "transcript_ref": "invalid"},
			}},
		})
	}))
	defer srv.Close()

	result := (&StatusProber{}).Probe(rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")})
	if !result.OK {
		t.Fatal("expected ok result")
	}
	if len(result.RunningSubagentIDs) != 1 || result.RunningSubagentIDs[0] != "child-running" {
		t.Fatalf("running subagent ids = %v, want [child-running]", result.RunningSubagentIDs)
	}
}

func fuzzScenarioStatusProber_AbsentPendingAskDecodesFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"session_id": "01A", "state": "active"})
	}))
	defer srv.Close()
	p := &StatusProber{}
	entry := rendezvous.Entry{Address: strings.TrimPrefix(srv.URL, "http://")}
	if p.Probe(entry).PendingAsk {
		t.Fatal("absent pending_ask (old daemon / Codex thread) must decode as false")
	}
}
