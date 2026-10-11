package server

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// The root row and thread/read carry the failure the session rests on while
// its status is systemError, and never otherwise (S1c). The status is the gate:
// the next turn moves the status first, so a summary sampled before that turn
// started cannot outlive the failure. TURN_ENDED re-samples the summary,
// because it re-samples every facet, so a second failure replaces the first.
func TestThreadSnapshotsCarryTheRestingFailureOnlyWhileFailed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	first := &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 401}}
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{failure: first})
	listed := func() *appwire.ThreadFailure {
		t.Helper()
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		return list.Data[0].Evener.Failure
	}

	srv.SetState(appwire.ThreadStatusIdle)
	if got := listed(); got != nil {
		t.Fatalf("an idle session's row carries the failure %+v", got)
	}
	srv.SetState(appwire.ThreadStatusSystemError)
	if got := listed(); !reflect.DeepEqual(got, first) {
		t.Fatalf("a failed session's row carries %+v, want %+v", got, first)
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener.Failure; !reflect.DeepEqual(got, first) {
		t.Fatalf("a failed session's thread/read carries %+v, want %+v", got, first)
	}

	second := &appwire.ThreadFailure{Title: "Usage limit reached", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 429}}
	src.failure = second
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root", Data: events.TurnEndedData{TurnDurationMS: 1_000}}, nil)
	if got := listed(); !reflect.DeepEqual(got, second) {
		t.Fatalf("after TURN_ENDED the row carries %+v, want %+v", got, second)
	}

	srv.SetState(appwire.ThreadStatusActive)
	if got := listed(); got != nil {
		t.Fatalf("a working session's row carries the failure %+v", got)
	}
}
