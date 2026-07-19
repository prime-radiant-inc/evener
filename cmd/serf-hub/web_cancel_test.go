package main

// Tests for POST /s/<id>/cancel-queued (issue #23): the hub REST fallback
// for the queue-preview row's cancel action (and the removal half of the
// row's edit action). The hub relays the index to the daemon's
// turn/cancelQueued and passes the removed-text echo back to the client;
// failures surface honestly (400 on a malformed body, 404 when the session
// isn't live, the daemon's Conflict when the index no longer resolves or
// the queue shifted under the client's snapshot, review F1).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// cancelRecordingSource is a scripted remote source that records the cancel
// params it was asked to relay.
type cancelRecordingSource struct {
	*scriptedAppSource
	gotIndex      int
	gotExpectedID string
	calls         int
	err           error
	removedText   string
	removedImages int
}

func (s *cancelRecordingSource) CancelQueued(_ context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	s.calls++
	s.gotIndex = params.Index
	s.gotExpectedID = params.ExpectedEntryID
	if s.err != nil {
		return appwire.TurnCancelQueuedResponse{}, s.err
	}
	return appwire.TurnCancelQueuedResponse{RemovedText: s.removedText, RemovedImages: s.removedImages}, nil
}

// newCancelThread uses the REALISTIC mid-turn capability set (review C1):
// Send is false while a turn is in flight — exactly when the queue preview
// and its cancel/edit buttons exist. An earlier draft of this test stubbed
// the impossible combination {Send:true, Steer:true, Queue:true}, which
// masked that gating cancel on the Send capability 503'd every real
// mid-turn cancel/edit.
func newCancelThread() appwire.Thread {
	return appwire.Thread{
		ID: "thread-1", SessionID: "thread-1", Source: "remote", Name: "cancel test",
		CWD: "/work/project", ModelProvider: "provider/model",
		Status: appwire.ThreadStatus{Type: "active"},
		Turns:  []appwire.Turn{{ID: "turn-1", Status: appwire.TurnStatusCompleted}},
		Serf: appwire.SerfThread{Ref: "remote:thread-1", ActiveTurnID: "turn-2",
			Capabilities: appwire.ThreadCapabilities{Send: false, Steer: true, Queue: true}},
	}
}

func newCancelHarness() (*WebServer, *cancelRecordingSource) {
	source := &cancelRecordingSource{
		scriptedAppSource: &scriptedAppSource{id: "remote", thread: newCancelThread()},
		gotIndex:          -1,
	}
	web := NewWebServer(hubcore.WebConfig{})
	registry := appsource.NewRegistry()
	registry.Add(source)
	web.sources = registry
	return web, source
}

func postCancel(web *WebServer, route, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	web.Handler().ServeHTTP(rec, req)
	return rec
}

// TestWebCancelQueuedWorksWithRealisticMidTurnCaps is the review-C1
// regression test: with a turn in flight the daemon projects Send=false,
// Steer=true, Queue=true. The REST path must reach the source (gated on
// Queue), not 503 with "send is not available for this session".
func TestWebCancelQueuedWorksWithRealisticMidTurnCaps(t *testing.T) {
	web, source := newCancelHarness()
	source.removedText = "mid-turn text"
	rec := postCancel(web, "/s/remote%3Athread-1/cancel-queued", `{"index":0,"entry_id":"q_1_aaa"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 with realistic mid-turn caps {Send:false,Steer:true,Queue:true}", rec.Code, rec.Body.String())
	}
	if source.calls != 1 {
		t.Fatalf("source called %d times, want 1", source.calls)
	}
}

func TestWebCancelQueuedRelaysIndexAndEchoesRemoval(t *testing.T) {
	web, source := newCancelHarness()
	source.removedText = "the full\nmulti-line text"
	source.removedImages = 2
	rec := postCancel(web, "/s/remote%3Athread-1/cancel-queued", `{"index":1,"entry_id":"q_7_abc"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if source.calls != 1 || source.gotIndex != 1 {
		t.Fatalf("cancel relay calls=%d index=%d, want 1/1", source.calls, source.gotIndex)
	}
	if source.gotExpectedID != "q_7_abc" {
		t.Fatalf("cancel relay expectedEntryId=%q, want q_7_abc", source.gotExpectedID)
	}
	var body cancelQueuedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v (%q)", err, rec.Body.String())
	}
	if body.RemovedText != "the full\nmulti-line text" || body.RemovedImages != 2 {
		t.Fatalf("response=%+v, want removed text + image count echoed", body)
	}
}

func TestWebCancelQueuedRejectsNegativeIndex(t *testing.T) {
	web, source := newCancelHarness()
	rec := postCancel(web, "/s/remote%3Athread-1/cancel-queued", `{"index":-1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if source.calls != 0 {
		t.Fatalf("source called %d times, want 0", source.calls)
	}
}

func TestWebCancelQueuedRejectsInvalidJSON(t *testing.T) {
	web, source := newCancelHarness()
	rec := postCancel(web, "/s/remote%3Athread-1/cancel-queued", `{"index":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if source.calls != 0 {
		t.Fatalf("source called %d times, want 0", source.calls)
	}
}

func TestWebCancelQueuedNotLive(t *testing.T) {
	// A local route id with no roster entry is not live; the hub must 404
	// before reaching any source.
	web, source := newCancelHarness()
	rec := postCancel(web, "/s/01MISSING/cancel-queued", `{"index":0}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if source.calls != 0 {
		t.Fatalf("source called %d times, want 0", source.calls)
	}
}

func TestWebCancelQueuedSurfacesDaemonConflict(t *testing.T) {
	web, source := newCancelHarness()
	source.err = appwire.Conflict("cancel: queue entry at index 0 no longer matches the snapshot (queue changed)")
	rec := postCancel(web, "/s/remote%3Athread-1/cancel-queued", `{"index":0}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if source.calls != 1 {
		t.Fatalf("source called %d times, want 1", source.calls)
	}
}

// TestHubRPCCancelQueuedRelays drives the appwire RPC path: the hub app
// server must route turn/cancelQueued to the source owning the ref,
// forwarding the index and echoing the removal, and reject a negative index
// client-side. The scripted thread carries the realistic mid-turn caps
// {Send:false, Steer:true, Queue:true} (review C1), so this also pins that
// the relay gates on Queue — a Send-gated relay would 503 here.
func TestHubRPCCancelQueuedRelays(t *testing.T) {
	_, source := newCancelHarness()
	source.removedText = "echo me"
	registry := appsource.NewRegistry()
	registry.Add(source)
	server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, registry)

	dispatch := func(params appwire.TurnCancelQueuedParams) (any, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("MarshalParams: %v", err)
		}
		return server.Router().Dispatch(context.Background(), appwire.Request{
			ID:     appwire.NewIntID(1),
			Method: appwire.MethodTurnCancelQueued,
			Params: raw,
		})
	}

	result, err := dispatch(appwire.TurnCancelQueuedParams{Ref: "remote:thread-1", Index: 2, ExpectedEntryID: "q_9_def"})
	if err != nil {
		t.Fatalf("dispatch cancel: %v", err)
	}
	if source.calls != 1 || source.gotIndex != 2 {
		t.Fatalf("cancel relay calls=%d index=%d, want 1/2", source.calls, source.gotIndex)
	}
	if source.gotExpectedID != "q_9_def" {
		t.Fatalf("cancel relay expectedEntryId=%q, want q_9_def", source.gotExpectedID)
	}
	resp, ok := result.(appwire.TurnCancelQueuedResponse)
	if !ok {
		t.Fatalf("result type=%T, want TurnCancelQueuedResponse", result)
	}
	if resp.RemovedText != "echo me" {
		t.Fatalf("removedText=%q, want echo me", resp.RemovedText)
	}

	_, err = dispatch(appwire.TurnCancelQueuedParams{Ref: "remote:thread-1", Index: -1})
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
