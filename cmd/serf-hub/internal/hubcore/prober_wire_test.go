package hubcore

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
	"primeradiant.com/serf/server"
)

// wireProbeEnvelopeSource is a minimal server.ThreadEnvelopeSource standing in
// for a live session, mirroring cmd/serf-hub/app_threadread_tasks_test.go's
// sessionTaskEnvelopeSource: every facet but the ones this test drives reports
// the zero value a daemon with nothing to say reports.
type wireProbeEnvelopeSource struct {
	askPending  bool
	escalations []appwire.SandboxEscalationRequested
	detailed    server.DetailedStatus
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
func (s wireProbeEnvelopeSource) GoalStatus() (string, int, bool)       { return "", 0, false }
func (s wireProbeEnvelopeSource) WorkMetrics() (int64, *appwire.SerfUsage, int64) {
	return 0, nil, 0
}
func (s wireProbeEnvelopeSource) FailedToolCalls() (int, bool) { return 0, false }
func (s wireProbeEnvelopeSource) AskPending() bool             { return s.askPending }
func (s wireProbeEnvelopeSource) PendingEscalations() []appwire.SandboxEscalationRequested {
	return s.escalations
}
func (s wireProbeEnvelopeSource) ReasoningInfo() (string, []string, bool) { return "", nil, false }
func (s wireProbeEnvelopeSource) SessionMeta() schema.SessionMeta         { return schema.SessionMeta{} }

// TestStatusProberAgreesWithServerStatusInfoAcrossTheWire decodes a REAL
// server.Server's /status response through the REAL StatusProber -- no
// hand-authored JSON on either end.
//
// server.StatusInfo (server/server.go) and this package's statusInfo
// (prober.go) are two independent declarations of the same wire contract by
// design (see prober.go's comment); nothing merges them. Every other pin
// proves just one side against a literal string authored IN THAT SAME
// PACKAGE: TestStatusWirePinsFailedToolCallsAndPendingEscalationJSONKeys
// (server package) decodes the server's own encode into an untyped map, and
// fuzzScenarioStatusProber_DecodesPendingAsk/_DecodesPendingEscalation (this
// package) feed the prober a hand-rolled literal. Both catch an uncoordinated
// rename, but neither catches a coordinated one: an edit that renames a tag
// in one declaration and "fixes" that same package's adjacent literal to
// match still passes both pins while the cross-process contract breaks,
// because neither literal is ever checked against the other declaration.
// This test removes the hand-authored literal from both ends, so a rename on
// EITHER declaration with no matching change on the other fails here
// regardless of which adjacent test the author remembered to update.
func TestStatusProberAgreesWithServerStatusInfoAcrossTheWire(t *testing.T) {
	srv := server.NewServer(server.ServerConfig{})
	srv.SetAppIdentity("local", "th_wire_1")
	srv.SetState("active")
	srv.SetThreadEnvelopeSource(wireProbeEnvelopeSource{
		askPending:  true,
		escalations: []appwire.SandboxEscalationRequested{{EscalationID: "esc_1", Tool: "read_file"}},
		detailed: server.DetailedStatus{Jobs: []server.JobStatusInfo{
			{JobType: "delegate", Status: "running", TranscriptRef: "local:child-1"},
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

	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()

	got := (&StatusProber{}).Probe(rendezvous.Entry{Address: strings.TrimPrefix(httpSrv.URL, "http://")})

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
		t.Errorf("running subagent ids = %v, want %v: the daemon must expose every projected descendant while retaining legacy Detailed.Jobs discovery", got.RunningSubagentIDs, want)
	}
	wantStates := map[string]string{"child-1": "active", "child-2": "idle", "grandchild-1": "active"}
	if !reflect.DeepEqual(got.RunningSubagentStates, wantStates) {
		t.Errorf("running subagent states = %v, want %v: server.StatusInfo.DescendantStates and the prober's descendant_states tag no longer agree", got.RunningSubagentStates, wantStates)
	}
}
