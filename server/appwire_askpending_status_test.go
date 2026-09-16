package server

import (
	"testing"

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
	other := appwire.AgentMessageDeltaParams{ThreadID: "th_1"}
	if got := srv.stampAskPendingOnStatusChange("some.other.method", other); got != other {
		t.Fatal("a notification that is not a status change must pass through")
	}
	if got := srv.stampAskPendingOnStatusChange(appwire.NotifyThreadStatusChanged, "wrong type"); got != "wrong type" {
		t.Fatal("params that are not status params must pass through")
	}
}
