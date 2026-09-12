package hubcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

type probeDaemonConfig struct {
	sessionID   string
	state       string
	hubToken    string
	source      wireProbeEnvelopeSource
	descendants map[string]string
}

func startProbeDaemon(t *testing.T, cfg probeDaemonConfig) (*StatusProber, rendezvous.Entry) {
	t.Helper()
	srv := server.NewServer(server.ServerConfig{HubToken: cfg.hubToken})
	srv.SetAppIdentity("local", cfg.sessionID)
	srv.SetState(cfg.state)
	srv.SetThreadEnvelopeSource(cfg.source)
	for id, state := range cfg.descendants {
		srv.RecordDescendantAppEvent(cfg.sessionID, events.SessionEvent{
			Kind:      events.EventUserInput,
			SessionID: id,
			Data:      events.UserInputData{Text: "probe fixture"},
		})
		if state != appwire.ThreadStatusActive {
			srv.RecordDescendantAppEvent(cfg.sessionID, events.SessionEvent{
				Kind:      events.EventSessionEnd,
				SessionID: id,
				Data:      events.SessionEndData{Reason: "input_complete", State: state},
			})
		}
	}
	srv.RefreshThreadEnvelope()
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	return &StatusProber{client: httpSrv.Client()}, rendezvous.Entry{
		Endpoint: "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/rpc",
		HubToken: cfg.hubToken,
	}
}

func TestHubProberStableDelegateUsesDescendantSessionsNotDetailedJobs(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "root",
		state:     appwire.ThreadStatusIdle,
		descendants: map[string]string{
			"child_stable": appwire.ThreadStatusActive,
		},
		source: wireProbeEnvelopeSource{detailed: server.DetailedStatus{Jobs: []server.JobStatusInfo{{
			JobID: "job_delegate", JobType: "delegate", Status: "running", TranscriptRef: "local:child_legacy",
		}}}},
	})
	got := prober.Probe(entry)
	if !got.OK {
		t.Fatal("probe failed")
	}
	if !reflect.DeepEqual(got.RunningSubagentIDs, []string{"child_stable"}) {
		t.Fatalf("descendants = %v, want typed thread/list descendants only", got.RunningSubagentIDs)
	}
	if !reflect.DeepEqual(got.RunningSubagentStates, map[string]string{"child_stable": "active"}) {
		t.Fatalf("descendant states = %#v", got.RunningSubagentStates)
	}
	if len(got.RunningJobs) != 0 {
		t.Fatalf("delegate job was duplicated as a running non-agent job: %+v", got.RunningJobs)
	}
}

func fuzzScenarioStatusProber_Success(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{sessionID: "01SESS001", state: appwire.ThreadStatusIdle})
	got := prober.Probe(entry)
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
	if p.Probe(rendezvous.Entry{Endpoint: "ws://127.0.0.1:1/rpc"}).OK { // port 1 not listening
		t.Fatal("expected ok=false on closed port")
	}
}

func fuzzScenarioStatusProber_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = conn.Write(context.Background(), websocket.MessageText, []byte("{not json"))
	}))
	defer srv.Close()
	p := &StatusProber{Timeout: 100 * time.Millisecond, client: srv.Client()}
	if p.Probe(rendezvous.Entry{Endpoint: "ws" + strings.TrimPrefix(srv.URL, "http")}).OK {
		t.Fatal("expected ok=false on malformed AppWire frame")
	}
}

func fuzzScenarioStatusProberSendsHubTokenBearer(t *testing.T) {
	const token = "secret-token"
	srv := server.NewServer(server.ServerConfig{HubToken: token})
	srv.SetAppIdentity("local", "01SESS001")
	srv.SetState(appwire.ThreadStatusIdle)
	gotAuth := make(chan string, 1)
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		srv.ServeHTTP(w, r)
	}))
	defer httpSrv.Close()
	p := &StatusProber{Timeout: 500 * time.Millisecond, client: httpSrv.Client()}
	entry := rendezvous.Entry{Endpoint: "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/rpc", HubToken: token}
	if !p.Probe(entry).OK {
		t.Fatal("expected ok=true")
	}
	select {
	case auth := <-gotAuth:
		if auth != "Bearer "+token {
			t.Fatalf("Authorization=%q, want bearer token", auth)
		}
	default:
		t.Fatal("server did not receive probe")
	}
}

func fuzzScenarioStatusProber_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	p := &StatusProber{Timeout: 100 * time.Millisecond, client: srv.Client()}
	got := p.Probe(rendezvous.Entry{Endpoint: "ws" + strings.TrimPrefix(srv.URL, "http") + "/rpc"})
	if got.OK || got.SessionID != "" || got.Status != "" {
		t.Fatalf("non-101 AppWire handshake produced status: %+v", got)
	}
}

func fuzzScenarioStatusProber_DecodesPendingAsk(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "01A", state: appwire.ThreadStatusAwaiting,
		source: wireProbeEnvelopeSource{askPending: true},
	})
	got := prober.Probe(entry)
	if !got.OK || got.SessionID != "01A" || got.Status != "awaiting" || !got.PendingAsk {
		t.Fatalf("Probe() = %+v; want 01A, awaiting, pending ask, ok", got)
	}
}

func fuzzScenarioStatusProber_DecodesPendingEscalation(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "01A", state: appwire.ThreadStatusActive,
		source: wireProbeEnvelopeSource{escalations: []appwire.SandboxEscalationRequested{{EscalationID: "esc_1"}}},
	})
	got := prober.Probe(entry)
	if !got.OK || got.SessionID != "01A" || got.Status != "active" || !got.PendingEscalation {
		t.Fatalf("Probe() pendingEscalation path = %+v; want 01A, active, pending escalation, ok", got)
	}
}

func fuzzScenarioStatusProber_AbsentPendingAskDecodesFalse(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{sessionID: "01A", state: appwire.ThreadStatusActive})
	if prober.Probe(entry).PendingAsk {
		t.Fatal("absent AppWire ask state must project false")
	}
}

func fuzzScenarioStatusProber_DecodesRunningSubagentIDs(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "parent", state: appwire.ThreadStatusIdle,
		descendants: map[string]string{"child-b": appwire.ThreadStatusActive, "child-a": appwire.ThreadStatusActive},
	})
	result := prober.Probe(entry)
	if !result.OK {
		t.Fatal("expected successful status probe")
	}
	want := []string{"child-a", "child-b"}
	if !reflect.DeepEqual(result.RunningSubagentIDs, want) {
		t.Fatalf("running subagent IDs = %v, want %v", result.RunningSubagentIDs, want)
	}
}

func TestProbeRunningSubagent(t *testing.T) { fuzzScenarioStatusProber_DecodesRunningSubagentIDs(t) }

func fuzzScenarioStatusProber_DecodesRunningSubagentStates(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "parent", state: appwire.ThreadStatusIdle,
		descendants: map[string]string{"child-a": appwire.ThreadStatusIdle, "child-b": appwire.ThreadStatusAwaiting},
	})
	result := prober.Probe(entry)
	if !result.OK {
		t.Fatal("expected successful status probe")
	}
	if want := []string{"child-a", "child-b"}; !reflect.DeepEqual(result.RunningSubagentIDs, want) {
		t.Fatalf("running subagent IDs = %v, want %v", result.RunningSubagentIDs, want)
	}
	wantStates := map[string]string{"child-a": "idle", "child-b": "awaiting"}
	if !reflect.DeepEqual(result.RunningSubagentStates, wantStates) {
		t.Fatalf("running subagent states = %v, want %v", result.RunningSubagentStates, wantStates)
	}
}

func TestProbeRunningSubagentStates(t *testing.T) {
	fuzzScenarioStatusProber_DecodesRunningSubagentStates(t)
}

func TestProbeIgnoresRetiredDetailedJobType(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "parent", state: appwire.ThreadStatusIdle,
		source: wireProbeEnvelopeSource{detailed: server.DetailedStatus{Jobs: []server.JobStatusInfo{{
			JobID: "job_delegate", JobType: "delegate", Status: "running", TranscriptRef: "local:child-type",
		}}}},
	})
	result := prober.Probe(entry)
	if len(result.RunningSubagentIDs) != 0 || len(result.RunningJobs) != 0 {
		t.Fatalf("delegate job inferred consumer status: %+v", result)
	}
}

func TestProbeSeparatesActiveAndCompletedNonAgentJobs(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "parent", state: appwire.ThreadStatusIdle,
		source: wireProbeEnvelopeSource{detailed: server.DetailedStatus{Jobs: []server.JobStatusInfo{
			{JobID: "job-running", JobType: "shell", Status: "running"},
			{JobID: "job-completed", JobType: "shell", Status: "completed"},
			{JobID: "job-delegate", JobType: "delegate", Status: "completed"},
		}}},
	})
	result := prober.Probe(entry)
	if !result.OK {
		t.Fatal("expected successful status probe")
	}
	if len(result.RunningJobs) != 1 || result.RunningJobs[0].JobID != "job-running" {
		t.Fatalf("running jobs = %+v, want only active non-agent job", result.RunningJobs)
	}
	if len(result.CompletedJobs) != 1 || result.CompletedJobs[0].JobID != "job-completed" {
		t.Fatalf("completed jobs = %+v, want only terminal non-agent job", result.CompletedJobs)
	}
}
