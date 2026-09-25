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
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
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

// An entry that names no session yet has only the answer to go on, so the
// probe cross-checks the listed root against a thread/read of the root: a
// daemon that re-bound the port between the two calls answers them for
// different sessions.
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

// An entry that names its session needs no thread/read: the listed row for
// that session is the root, and a list that carries no such row is another
// daemon's answer. The probe must not spend a second full root snapshot on
// an identity the entry already states.
func TestStatusProberTakesANamedRootFromTheListAlone(t *testing.T) {
	var reads int
	var listParams []appwire.ThreadListParams
	rpc := appserver.NewServer(appserver.ServerConfig{ServerName: "status-test", SourceID: "local"})
	appserver.HandleTyped(rpc.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		reads++
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "root-b", SessionID: "root-b", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}}, nil
	})
	appserver.HandleTyped(rpc.Router(), appwire.MethodThreadList, func(_ context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		listParams = append(listParams, params)
		return appwire.ThreadListResponse{Data: []appwire.Thread{
			{ID: "root-a", SessionID: "root-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
			{ID: "child-1", SessionID: "child-1", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
		}}, nil
	})
	httpSrv := httptest.NewServer(http.HandlerFunc(rpc.ServeWebSocket))
	defer httpSrv.Close()
	prober := &StatusProber{client: httpSrv.Client()}
	endpoint := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	got := prober.Probe(rendezvous.Entry{Endpoint: endpoint, SessionID: "root-a"})
	if !got.OK || got.SessionID != "root-a" || got.Status != appwire.ThreadStatusActive {
		t.Fatalf("probe of an entry naming root-a = %+v, want the listed root-a row", got)
	}
	if want := []string{"child-1"}; !reflect.DeepEqual(got.RunningSubagentIDs, want) {
		t.Fatalf("running subagent ids = %v, want %v: the named root must not be counted as a child", got.RunningSubagentIDs, want)
	}
	if reads != 0 {
		t.Fatalf("thread/read calls = %d, want 0 when the entry names its session", reads)
	}
	// The probe asks for the status-only answer; a daemon that predates the
	// field ignores it and returns the full one, which reads the same.
	if want := []appwire.ThreadListParams{{IncludeSubagents: true, StatusOnly: true}}; !reflect.DeepEqual(listParams, want) {
		t.Fatalf("thread/list params = %+v, want %+v", listParams, want)
	}
	if got := prober.Probe(rendezvous.Entry{Endpoint: endpoint, SessionID: "root-c"}); got.OK {
		t.Fatalf("a list with no row for the named session produced a live probe: %+v", got)
	}
}

// The answer a probe gets carries the answering daemon's session id (its
// root thread's SessionID, also its Evener.InstanceID), and a daemon keeps its
// rendezvous entry's session id current (rvreg.UpdateSessionID). So an
// endpoint answering for a session the entry does not name is another
// daemon that re-bound the port; its answer must not be published under the
// entry.
func TestStatusProberRejectsAnAnswerForAnotherSession(t *testing.T) {
	other := appwire.Thread{ID: "other", SessionID: "other", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}
	rpc := appserver.NewServer(appserver.ServerConfig{ServerName: "status-test", SourceID: "local"})
	appserver.HandleTyped(rpc.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: other}, nil
	})
	appserver.HandleTyped(rpc.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{other}}, nil
	})
	httpSrv := httptest.NewServer(http.HandlerFunc(rpc.ServeWebSocket))
	defer httpSrv.Close()
	prober := &StatusProber{client: httpSrv.Client()}
	endpoint := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	if got := prober.Probe(rendezvous.Entry{Endpoint: endpoint, SessionID: "mine", ThreadID: "mine"}); got.OK {
		t.Fatalf("an answer for session %q was published under an entry naming %q: %+v", other.SessionID, "mine", got)
	}
	// An entry that names no session takes the answer's, as it always has.
	if got := prober.Probe(rendezvous.Entry{Endpoint: endpoint}); !got.OK || got.SessionID != "other" {
		t.Fatalf("an entry naming no session did not take the answer's: %+v", got)
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
		Kind:      events.EventExecutionStarted,
		SessionID: "child-1",
		Data:      events.ExecutionStartedData{TurnID: "t_probe"},
	})
	srv.RecordDescendantAppEvent("th_wire_1", events.SessionEvent{
		Kind:      events.EventExecutionStarted,
		SessionID: "child-2",
		Data:      events.ExecutionStartedData{TurnID: "t_probe"},
	})
	srv.RecordDescendantAppEvent("th_wire_1", events.SessionEvent{
		Kind:      events.EventExecutionStarted,
		SessionID: "grandchild-1",
		Data:      events.ExecutionStartedData{TurnID: "t_probe"},
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

// TestStatusProberCarriesDaemonCapabilities pins the other half of thread
// parity: beside the status the probe already carries, it must carry the
// daemon's own Evener capabilities from the same projection cut, so the
// hub's list rows can advertise the daemon's answer rather than a hand
// approximation. The set comes from the LISTED root, so it is the same
// snapshot cut as the status and the diagnostics the probe carries.
func TestStatusProberCarriesDaemonCapabilities(t *testing.T) {
	// Wire the production seam set so the daemon's idle answer is the
	// production shape: every capability but fork is true at idle.
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_wire_caps",
		state:     appwire.ThreadStatusIdle,
		setup:     hubtest.WireCapabilitySeams,
	})
	got := prober.Probe(entry)
	if !got.OK {
		t.Fatal("expected ok=true probing a real server")
	}
	if !got.CapabilitiesKnown {
		t.Fatal("CapabilitiesKnown = false, want true: the probe must carry the daemon's capabilities answer")
	}
	want := appwire.ThreadCapabilities{
		Send: true, Steer: true, Interrupt: true, Queue: true,
		Compact: true, Clear: true, Shutdown: true, ChangeModel: true,
		ChangeVisionModel: true, Rename: true, Goal: true, SharedNotes: true,
		SkillInput: true, // ForkFromTurn stays the daemon's hardwired false.
	}
	if got.Capabilities != want {
		t.Fatalf("capabilities = %+v, want the daemon's idle set %+v", got.Capabilities, want)
	}
}

func TestStatusProberProjectsQuiescedStableDelegateAsIdle(t *testing.T) {
	// A retained child can still have an active descendant projection even after
	// its stable delegate run has settled. The stable delegate lifecycle is the
	// authoritative no-current-run signal for the parent-side projection.
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_wire_quiesced",
		descendants: map[string]string{
			"child-quiesced": appwire.ThreadStatusActive,
		},
		source: wireProbeEnvelopeSource{detailed: server.DetailedStatus{Delegates: []server.DelegateStatusInfo{{
			DelegateID: "dlg_quiesced", ChildSessionID: "child-quiesced",
			Lifecycle: "idle", Phase: "idle", Status: "idle", Resumable: true,
		}}}},
	})
	got := prober.Probe(entry)
	if !got.OK {
		t.Fatal("expected ok=true probing a real server")
	}
	if want := []string{"child-quiesced"}; !reflect.DeepEqual(got.RunningSubagentIDs, want) {
		t.Fatalf("running subagent ids = %v, want %v so the retained child remains visible", got.RunningSubagentIDs, want)
	}
	if got.RunningSubagentStates["child-quiesced"] != appwire.ThreadStatusIdle {
		t.Fatalf("quiesced child state = %q, want idle from stable delegate diagnostics", got.RunningSubagentStates["child-quiesced"])
	}
}

func TestStatusProberPreservesRunningStableDelegateAsActive(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_wire_running",
		descendants: map[string]string{
			"child-running": appwire.ThreadStatusActive,
		},
		source: wireProbeEnvelopeSource{detailed: server.DetailedStatus{Delegates: []server.DelegateStatusInfo{{
			DelegateID: "dlg_running", ChildSessionID: "child-running",
			Lifecycle: "running", Phase: "running", Status: "running", Resumable: true,
		}}}},
	})
	got := prober.Probe(entry)
	if !got.OK {
		t.Fatal("expected ok=true probing a real server")
	}
	if got.RunningSubagentStates["child-running"] != appwire.ThreadStatusActive {
		t.Fatalf("running child state = %q, want active from the child projection", got.RunningSubagentStates["child-running"])
	}
}

func TestStatusProberDoesNotMaskChildResumeBetweenSnapshots(t *testing.T) {
	rpc := appserver.NewServer(appserver.ServerConfig{ServerName: "status-test", SourceID: "local"})
	var calls []string
	record := func(name string) {
		calls = append(calls, name)
	}
	root := func(lifecycle string) appwire.Thread {
		return appwire.Thread{
			ID: "root", SessionID: "root", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Diagnostics: &appwire.EvenerDiagnostics{
				Delegates: []appwire.EvenerDelegateInfo{{
					ChildSessionID: "child-resumed", Lifecycle: lifecycle, Phase: lifecycle, Status: lifecycle,
				}},
			}},
		}
	}
	appserver.HandleTyped(rpc.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		record("list")
		return appwire.ThreadListResponse{Data: []appwire.Thread{
			root(""),
			{ID: "child-resumed", SessionID: "child-resumed", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
		}}, nil
	})
	appserver.HandleTyped(rpc.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		record("read")
		listWasFirst := len(calls) > 0 && calls[0] == "list"
		lifecycle := "idle"
		if listWasFirst {
			lifecycle = "running"
		}
		return appwire.ThreadReadResponse{Thread: root(lifecycle)}, nil
	})
	httpSrv := httptest.NewServer(http.HandlerFunc(rpc.ServeWebSocket))
	defer httpSrv.Close()
	prober := &StatusProber{client: httpSrv.Client()}
	endpoint := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	// An entry naming no session cross-checks the root with a thread/read,
	// which must follow the list so a later running lifecycle cannot be
	// mistaken for stale active work.
	got := prober.Probe(rendezvous.Entry{Endpoint: endpoint})
	if !got.OK {
		t.Fatal("expected ok=true probing a real server")
	}
	if got.RunningSubagentStates["child-resumed"] != appwire.ThreadStatusActive {
		t.Fatalf("resumed child state = %q, want active from the newer lifecycle snapshot", got.RunningSubagentStates["child-resumed"])
	}
	if !reflect.DeepEqual(calls, []string{"list", "read"}) {
		t.Fatalf("probe snapshot calls = %v, want list before read to avoid masking a resume", calls)
	}

	// An entry naming its session takes root and children from the one list
	// cut, so no second snapshot can disagree with it.
	calls = nil
	got = prober.Probe(rendezvous.Entry{Endpoint: endpoint, SessionID: "root"})
	if !got.OK {
		t.Fatal("expected ok=true probing a real server for a named session")
	}
	if got.RunningSubagentStates["child-resumed"] != appwire.ThreadStatusActive {
		t.Fatalf("resumed child state = %q, want active from the listed child", got.RunningSubagentStates["child-resumed"])
	}
	if !reflect.DeepEqual(calls, []string{"list"}) {
		t.Fatalf("probe snapshot calls = %v, want the list alone for a named session", calls)
	}
}
