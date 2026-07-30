package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
)

// statusNotifications pulls every thread/status/changed the server broadcast.
func statusNotifications(t *testing.T, srv *Server, threadID string) []appwire.ThreadStatusChangedParams {
	t.Helper()
	var out []appwire.ThreadStatusChangedParams
	for _, n := range srv.AppNotificationsAfter(0, threadID) {
		if n.Notification.Method != appwire.NotifyThreadStatusChanged {
			continue
		}
		var params appwire.ThreadStatusChangedParams
		if err := json.Unmarshal(n.Notification.Params, &params); err != nil {
			t.Fatalf("decode thread/status/changed params: %v", err)
		}
		out = append(out, params)
	}
	return out
}

// A watching client holds the count as a snapshot until the next thread/read,
// so without a push a session that was clean when the client attached and
// failed later keeps saying nothing — the exact reader the count was built for
// (kata 12rq). Every status transition is a turn boundary, so the figure rides
// along there.
func TestStatusChangeCarriesTheRunningFailureCount(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishFailureCount(srv, 0)

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})
	publishFailureCount(srv, 4)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})

	statuses := statusNotifications(t, srv, "th_1")
	if len(statuses) == 0 {
		t.Fatal("no thread/status/changed notifications recorded")
	}
	last := statuses[len(statuses)-1]
	if last.FailedToolCalls == nil {
		t.Fatal("last thread/status/changed carried no failure count, want the running figure")
	}
	if got := *last.FailedToolCalls; got != 4 {
		t.Fatalf("thread/status/changed FailedToolCalls = %d, want 4", got)
	}
}

func TestStatusChangeOmitsAnUnmeasuredFailureCount(t *testing.T) {
	// Unmeasured must not ride along as a zero: a client that took it would
	// then be showing a clean run it was never told about. Absence on this
	// notification means "no update" and leaves whatever the hydrate said.
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.failedToolCalls = 0; e.failuresMeasured = false })

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})

	for _, params := range statusNotifications(t, srv, "th_1") {
		if params.FailedToolCalls != nil {
			t.Fatalf("thread/status/changed carried %d for an unmeasured session, want absent", *params.FailedToolCalls)
		}
	}
}

func TestStatusChangeOmitsTheCountOnADaemonThatNeverWiredIt(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})

	for _, params := range statusNotifications(t, srv, "th_1") {
		if params.FailedToolCalls != nil {
			t.Fatalf("thread/status/changed carried %d with no callback wired, want absent", *params.FailedToolCalls)
		}
	}
}
