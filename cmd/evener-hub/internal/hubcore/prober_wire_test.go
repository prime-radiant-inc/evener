package hubcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// wireProbeEnvelopeSource is a minimal server.ThreadEnvelopeSource standing in
// for a live session, mirroring cmd/evener-hub/app_threadread_tasks_test.go's
// sessionTaskEnvelopeSource: every facet but the ones this test drives reports
// the zero value a daemon with nothing to say reports.
type wireProbeEnvelopeSource struct {
	askPending  bool
	escalations []appwire.SandboxEscalationRequested
	detailed    server.DetailedStatus
}

func TestStatusProberRejectsMismatchedRootSnapshots(t *testing.T) {
	rpc := appserver.NewServer(appserver.ServerConfig{ServerName: "status-test", SourceID: "local"})
	appserver.HandleTyped(rpc.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "root-a", SessionID: "root-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}}, nil
	})
	appserver.HandleTyped(rpc.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "root-b", SessionID: "root-b", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}}}, nil
	})
	httpSrv := httptest.NewServer(http.HandlerFunc(rpc.ServeWebSocket))
	defer httpSrv.Close()

	got := (&StatusProber{client: httpSrv.Client()}).Probe(rendezvous.Entry{
		Endpoint: "ws" + strings.TrimPrefix(httpSrv.URL, "http"),
	})
	if got.OK {
		t.Fatalf("mismatched thread/read and thread/list roots produced a live probe: %+v", got)
	}
}

func (s wireProbeEnvelopeSource) ContextPressure() float64 { return 0 }
func (s wireProbeEnvelopeSource) ContextMetrics() server.ContextMetrics {
	return server.ContextMetrics{}
}
func (s wireProbeEnvelopeSource) DetailedStatus() server.DetailedStatus { return s.detailed }
func (s wireProbeEnvelopeSource) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	return appwire.QueueState{}, nil
}
func (s wireProbeEnvelopeSource) TaskAggregate() *appwire.TaskAggregate { return nil }
func (s wireProbeEnvelopeSource) GoalStatus() (string, string, int, bool) {
	return "", "", 0, false
}
func (s wireProbeEnvelopeSource) WorkMetrics() (int64, *appwire.EvenerUsage, int64) {
	return 0, nil, 0
}
func (s wireProbeEnvelopeSource) FailedToolCalls() (int, bool) { return 0, false }
func (s wireProbeEnvelopeSource) AskPending() bool             { return s.askPending }
func (s wireProbeEnvelopeSource) PendingEscalations() []appwire.SandboxEscalationRequested {
	return s.escalations
}
func (s wireProbeEnvelopeSource) ReasoningInfo() (string, []string, bool) { return "", nil, false }
func (s wireProbeEnvelopeSource) VisionModel() string                     { return "" }
func (s wireProbeEnvelopeSource) SessionMeta() schema.SessionMeta         { return schema.SessionMeta{} }

// TestStatusProberReadsAppWireStatusIncludingNonAgentJobs drives a real daemon
// AppWire server through the real typed client. The shell row proves status is
// not inferred only from delegate descendants; the legacy delegate job row
// proves descendants remain the sole agent projection and are not duplicated.
func TestStatusProberReadsAppWireStatusIncludingNonAgentJobs(t *testing.T) {
	srv := server.NewServer(server.ServerConfig{})
	srv.SetAppIdentity("local", "th_wire_1")
	srv.SetState("active")
	srv.SetThreadEnvelopeSource(wireProbeEnvelopeSource{
		askPending:  true,
		escalations: []appwire.SandboxEscalationRequested{{EscalationID: "esc_1", Tool: "read_file"}},
		detailed: server.DetailedStatus{Jobs: []server.JobStatusInfo{
			{JobID: "job_shell", JobType: "shell", Status: "running", OutputBytes: 17},
			{JobID: "job_done", JobType: "shell", Status: "completed"},
			{JobID: "job_delegate", JobType: "delegate", Status: "running", TranscriptRef: "local:child-1"},
		}},
	})
	srv.RefreshThreadEnvelope()
	// The root daemon projects every in-process descendant, including nested
	// descendants whose delegate jobs are owned by an intermediate child and are
	// therefore absent from the root session's Detailed.Jobs.
	srv.RecordDescendantAppEvent("th_wire_1", events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "child-1",
		Data:      events.UserInputData{Text: "legacy job duplicate"},
	})
	srv.RecordDescendantAppEvent("th_wire_1", events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "child-2",
		Data:      events.UserInputData{Text: "direct child"},
	})
	srv.RecordDescendantAppEvent("th_wire_1", events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "grandchild-1",
		Data:      events.UserInputData{Text: "nested child"},
	})
	// A settled descendant ends its turn idle; its liveness (it stays in
	// descendant_session_ids, resumable) must not read as activity.
	srv.RecordDescendantAppEvent("th_wire_1", events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "child-2",
		Data:      events.SessionEndData{Reason: "input_complete", State: "idle"},
	})
	srv.RecordDescendantAppEvent("th_wire_1", events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "closed-child",
		Data:      events.SessionEndData{Reason: "shutdown", State: "closed"},
	})

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			http.NotFound(w, r)
			return
		}
		srv.ServeHTTP(w, r)
	}))
	defer httpSrv.Close()

	got := (&StatusProber{client: httpSrv.Client()}).Probe(rendezvous.Entry{
		Address:  strings.TrimPrefix(httpSrv.URL, "http://"),
		Protocol: appwire.ProtocolVersion,
		Endpoint: "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/rpc",
	})

	if !got.OK {
		t.Fatal("expected ok=true probing a real server")
	}
	if got.SessionID != "th_wire_1" {
		t.Errorf(`session_id = %q, want "th_wire_1": server.StatusInfo.SessionID and the prober's session_id tag no longer agree`, got.SessionID)
	}
	if got.Status != "active" {
		t.Errorf(`state = %q, want "active": server.StatusInfo.State and the prober's state tag no longer agree`, got.Status)
	}
	if !got.PendingAsk {
		t.Error("pending_ask = false, want true: server.StatusInfo.PendingAsk and the prober's pending_ask tag no longer agree")
	}
	if !got.PendingEscalation {
		t.Error("pending_escalation = false, want true: server.StatusInfo.PendingEscalation and the prober's pending_escalation tag no longer agree — the hub's needs-you badge would go dark")
	}
	if want := []string{"child-1", "child-2", "grandchild-1"}; !reflect.DeepEqual(got.RunningSubagentIDs, want) {
		t.Errorf("running subagent ids = %v, want %v: the daemon must expose every projected descendant through descendant_session_ids", got.RunningSubagentIDs, want)
	}
	wantStates := map[string]string{"child-1": "active", "child-2": "idle", "grandchild-1": "active"}
	if !reflect.DeepEqual(got.RunningSubagentStates, wantStates) {
		t.Errorf("running subagent states = %v, want %v", got.RunningSubagentStates, wantStates)
	}
	if len(got.RunningJobs) != 1 {
		t.Fatalf("running jobs = %+v, want only the non-agent shell job", got.RunningJobs)
	}
	job := got.RunningJobs[0]
	if job.JobID != "job_shell" || job.JobType != "shell" || job.Status != "running" || job.OutputBytes != 17 {
		t.Fatalf("running job = %+v, want shell identity and status from Evener.Diagnostics.Jobs", job)
	}
}
