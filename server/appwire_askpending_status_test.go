package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"

	"primeradiant.com/evener/appwire"
)

// askPending is otherwise snapshot-only: the hub stamps it into thread
// snapshots and no notification carries a change, so every client keeps
// showing "question waiting" after the answer until its next read (#1613).
// Every clear of the pending set is a turn boundary — a resolving user turn or
// an interrupt — which is exactly when thread/status/changed is announced, so
// the flag rides that frame, like the failure count and the capability set
// beside it.
func TestStatusChangeCarriesAskPendingWhileAQuestionWaits(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.askPending = true })

	stamped, ok := srv.stampAskPendingOnStatusChange(appwire.NotifyThreadStatusChanged,
		appwire.ThreadStatusChangedParams{ThreadID: "th_1"}).(appwire.ThreadStatusChangedParams)
	if !ok {
		t.Fatal("stamping a status change did not answer with status params")
	}
	if stamped.AskPending == nil || !*stamped.AskPending {
		t.Fatalf("askPending=%v, want the waiting question announced", stamped.AskPending)
	}
}

// The clear is the case the issue is about: the answer resolves the question,
// the pending set empties, and the status frame that goes with the resumed turn
// says so, so no client has to reread to stop saying "question waiting".
func TestStatusChangeCarriesAskPendingClearedByTheAnswer(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.askPending = true })
	// The answer: the pending set empties and the turn resumes.
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.askPending = false })
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "working"})

	stamped, ok := srv.stampAskPendingOnStatusChange(appwire.NotifyThreadStatusChanged,
		appwire.ThreadStatusChangedParams{ThreadID: "th_1"}).(appwire.ThreadStatusChangedParams)
	if !ok {
		t.Fatal("stamping a status change did not answer with status params")
	}
	if stamped.AskPending == nil {
		t.Fatal("askPending is absent, so a client cannot tell the question was answered")
	}
	if *stamped.AskPending {
		t.Fatal("askPending=true after the answer, want the clear announced")
	}
}

// Every other notification and every other params shape passes through, like
// the two stampers beside this one.
func TestStampAskPendingLeavesEverythingElseAlone(t *testing.T) {
	srv := NewServer(ServerConfig{})
	other := appwire.OverlayDeltaParams{ThreadID: "th_1"}
	if got := srv.stampAskPendingOnStatusChange("some.other.method", other); got != other {
		t.Fatal("a notification that is not a status change must pass through")
	}
	if got := srv.stampAskPendingOnStatusChange(appwire.NotifyThreadStatusChanged, "wrong type"); got != "wrong type" {
		t.Fatal("params that are not status params must pass through")
	}
}

// The stamper above is only reached if both notification egress loops call it
// (appwire_runtime.go:565, :634), so this drives a real session event through
// the server and reads the frame it recorded — the shape
// appwire_failure_push_test.go and appwire_capabilities_push_test.go use for the
// two fields that ride this frame beside askPending. Dropping the call from
// either loop fails here.
func TestRecordedStatusFrameCarriesAskPending(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.askPending = true })

	startExecution(srv, "th_1", "t_go")

	statuses := statusNotifications(t, srv, "th_1")
	if len(statuses) == 0 {
		t.Fatal("no thread/status/changed was recorded for the turn going active")
	}
	active := statuses[len(statuses)-1]
	if active.AskPending == nil {
		t.Fatal("the recorded status frame carries no askPending, so a client cannot learn the flag moved")
	}
	if !*active.AskPending {
		t.Fatal("the recorded status frame says no question is waiting, want the envelope's own value")
	}

	// And the clear: the answer empties the pending set, and the frame that goes
	// with the next transition says so.
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.askPending = false })
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "th_1",
		Data:      events.SessionEndData{Reason: "input_complete", State: "idle"},
	})

	statuses = statusNotifications(t, srv, "th_1")
	settled := statuses[len(statuses)-1]
	if settled.AskPending == nil || *settled.AskPending {
		t.Fatalf("recorded askPending=%v after the answer, want an explicit false", settled.AskPending)
	}
}
