package main

// Tests for POST /s/<id>/promote-queued (issue #22): the hub REST fallback
// for the queue-preview row's promote action. The hub relays the index to
// the daemon's turn/promoteQueuedAsSteer; failures surface honestly (400 on
// a malformed body, 404 when the session isn't live, the daemon's Conflict
// when the turn already ended or the index no longer resolves).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// promoteRecordingSource is a scripted remote source that records the
// promote params it was asked to relay.
type promoteRecordingSource struct {
	*scriptedAppSource
	gotIndex      int
	gotExpectedID string
	calls         int
	err           error
}

func (s *promoteRecordingSource) PromoteQueuedAsSteer(_ context.Context, params appwire.TurnPromoteQueuedAsSteerParams) error {
	s.calls++
	s.gotIndex = params.Index
	s.gotExpectedID = params.ExpectedEntryID
	return s.err
}

func newPromoteWeb(source *promoteRecordingSource) *WebServer {
	web := NewWebServer(hubcore.WebConfig{})
	registry := appsource.NewRegistry()
	registry.Add(source)
	web.sources = registry
	return web
}

func newPromoteThread() appwire.Thread {
	return appwire.Thread{
		ID: "thread-1", SessionID: "thread-1", Source: "remote", Name: "promote test",
		CWD: "/work/project", ModelProvider: "provider/model",
		Status: appwire.ThreadStatus{Type: "active"},
		Turns:  []appwire.Turn{{ID: "turn-1", Status: appwire.TurnStatusCompleted}},
		Serf: appwire.SerfThread{Ref: "remote:thread-1", ActiveTurnID: "turn-2",
			Capabilities: appwire.ThreadCapabilities{Steer: true, Queue: true}},
	}
}

func newPromoteHarness() (*WebServer, *promoteRecordingSource) {
	source := &promoteRecordingSource{
		scriptedAppSource: &scriptedAppSource{id: "remote", thread: newPromoteThread()},
		gotIndex:          -1,
	}
	return newPromoteWeb(source), source
}

// TestHubRPCPromoteQueuedAsSteerRelays drives the appwire RPC path: the hub
// app server must route turn/promoteQueuedAsSteer to the source owning the
// ref, forwarding the index, and reject a negative index client-side.
func TestHubRPCPromoteQueuedAsSteerRelays(t *testing.T) {
	_, source := newPromoteHarness()
	registry := appsource.NewRegistry()
	registry.Add(source)
	server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, registry)

	dispatch := func(params appwire.TurnPromoteQueuedAsSteerParams) error {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("MarshalParams: %v", err)
		}
		_, derr := server.Router().Dispatch(context.Background(), appwire.Request{
			ID:     appwire.NewIntID(1),
			Method: appwire.MethodTurnPromoteQueuedAsSteer,
			Params: raw,
		})
		return derr
	}

	if err := dispatch(appwire.TurnPromoteQueuedAsSteerParams{Ref: "remote:thread-1", Index: 2, ExpectedEntryID: "q_9_def"}); err != nil {
		t.Fatalf("dispatch promote: %v", err)
	}
	if source.calls != 1 || source.gotIndex != 2 {
		t.Fatalf("promote relay calls=%d index=%d, want 1/2", source.calls, source.gotIndex)
	}
	if source.gotExpectedID != "q_9_def" {
		t.Fatalf("promote relay expectedEntryId=%q, want q_9_def", source.gotExpectedID)
	}

	err := dispatch(appwire.TurnPromoteQueuedAsSteerParams{Ref: "remote:thread-1", Index: -1})
	if err == nil {
		t.Fatal("expected error for negative index")
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("err=%v, want InvalidParams wire error", err)
	}
	if source.calls != 1 {
		t.Fatalf("source called %d times after negative index, want 1", source.calls)
	}
}
