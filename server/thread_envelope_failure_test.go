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

// A model call waiting out a usage limit keeps its session working, so the
// failure gate above would hide the wait. A summary that names when the call
// resumes is shown on a working row too, so the row can say the session waits
// for quota. The wait's start (MODEL_RETRY) samples it, and the retry that
// ends it (another MODEL_RETRY, with no delay), the call's next answer
// (ASSISTANT_TEXT_END) or a model switch re-samples it once the wait is over.
func TestThreadSnapshotsCarryAQuotaWaitWhileWorking(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{})
	listed := func() *appwire.ThreadFailure {
		t.Helper()
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		return list.Data[0].Evener.Failure
	}
	srv.SetState(appwire.ThreadStatusActive)

	waiting := &appwire.ThreadFailure{Title: "Usage limit reached", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Status: 429}, ResumesAt: 1_800_000_000_000}
	for _, ended := range []events.EventData{
		// The retry that ends the wait is announced as it goes out, so the
		// row drops the wait before the retry's response streams.
		events.ModelRetryData{Attempt: 1},
		events.AssistantTextEndData{Text: "back at work"},
		events.ModelChangedData{NewProvider: "openai", NewModel: "gpt-5.4"},
	} {
		src.failure = waiting
		BridgeEvent(srv, events.SessionEvent{Kind: events.EventModelRetry, SessionID: "root", Data: events.ModelRetryData{Attempt: 1, DelayMS: 7_200_000}}, nil)
		if got := listed(); !reflect.DeepEqual(got, waiting) {
			t.Fatalf("a working session waiting for quota lists %+v, want %+v", got, waiting)
		}
		if got := readThreadOverWire(t, srv, "local:root").Evener.Failure; !reflect.DeepEqual(got, waiting) {
			t.Fatalf("a working session waiting for quota reads %+v, want %+v", got, waiting)
		}

		src.failure = nil
		BridgeEvent(srv, events.SessionEvent{Kind: events.KindOf(ended), SessionID: "root", Data: ended}, nil)
		if got := listed(); got != nil {
			t.Fatalf("after %s the row still carries the wait %+v", events.KindOf(ended), got)
		}
	}
}

// A delegate waits out a usage limit like any turn, but its row is served by
// the parent daemon: thread/list attaches the wait live from the session
// tree, and a delegate's own thread/read serves its stored projection. A
// pane opened on the delegate - or reloaded while it waits - reads that
// projection, so the projection must carry the wait's summary the same way,
// and drop it when the wait ends.
func TestDescendantReadCarriesAQuotaWaitWhileWorking(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_root")
	srv.RecordDescendantAppEvent("th_root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "th_child"})
	read := func() *appwire.ThreadFailure {
		t.Helper()
		response, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{ThreadID: "th_child"})
		if err != nil {
			t.Fatalf("thread/read: %v", err)
		}
		return response.Thread.Evener.Failure
	}

	// The wait begins: the MODEL_RETRY announcement whose delay runs to the
	// reset must reach the delegate's stored projection.
	srv.RecordDescendantAppEvent("th_root", events.SessionEvent{
		Kind:      events.EventModelRetry,
		SessionID: "th_child",
		Data:      events.ModelRetryData{Attempt: 1, DelayMS: 7_200_000, ErrorClass: "quota_exceeded"},
	})
	if got := read(); got == nil || got.ResumesAt == 0 {
		t.Fatalf("a waiting delegate's read carries %+v, want the usage limit and its resume time", got)
	}

	// The wait ends (the retry goes out): the zero-delay announcement must
	// drop it, so a reload during the retry shows the delegate working, not
	// still waiting.
	srv.RecordDescendantAppEvent("th_root", events.SessionEvent{
		Kind:      events.EventModelRetry,
		SessionID: "th_child",
		Data:      events.ModelRetryData{Attempt: 1, DelayMS: 0, ErrorClass: "quota_exceeded"},
	})
	if got := read(); got != nil {
		t.Fatalf("after the retry starts the delegate's read still carries the wait %+v", got)
	}
}
