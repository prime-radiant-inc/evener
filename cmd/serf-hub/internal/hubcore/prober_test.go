package hubcore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/rendezvous"
)

type statusStub struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

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
		t.Fatalf("Probe() pendingEscalation path = %+v; want 01A, active, true, true", got)
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

func fuzzScenarioStatusProber_DecodesRunningSubagentIDs(t *testing.T) {
	payload := `{"session_id":"parent","state":"idle","detailed":{"jobs":[{"job_type":"delegate","status":"running","transcript_ref":"local:child-b"},{"job_type":"delegate","status":"completed","transcript_ref":"local:child-done"},{"job_type":"shell","status":"running","transcript_ref":"local:not-a-child"},{"job_type":"delegate","status":"running","transcript_ref":"remote:child-remote"},{"job_type":"delegate","status":"running","transcript_ref":"invalid"},{"job_type":"delegate","status":"running","transcript_ref":"local:child-a"},{"job_type":"delegate","status":"running","transcript_ref":"local:child-a"}]}}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}
	result := (&StatusProber{client: client}).Probe(rendezvous.Entry{Address: "status.test"})
	if !result.OK {
		t.Fatal("expected successful status probe")
	}
	want := []string{"child-a", "child-b"}
	if !reflect.DeepEqual(result.RunningSubagentIDs, want) {
		t.Fatalf("running subagent IDs = %v, want %v", result.RunningSubagentIDs, want)
	}
}

func TestProbeRunningSubagent(t *testing.T) { fuzzScenarioStatusProber_DecodesRunningSubagentIDs(t) }
