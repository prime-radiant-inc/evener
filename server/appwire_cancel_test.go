package server

// Tests for the turn/cancelQueued appwire method (issue #23): the daemon
// removes the queued follow-up at the requested index so it is never
// consumed, echoing the removed entry's full text and image count. Unlike
// turn/promoteQueuedAsSteer, cancel does NOT require an active turn — a
// queued entry is cancellable whenever it is still queued. Failures are
// honest: closed → Conflict, negative index → InvalidParams, no callback →
// Unavailable, and a session-side rejection (the index fell out of range or
// the expected entry id no longer matches, review F1) propagates as
// Conflict.

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func cancelRPC(conn interface {
	HandleMessage(context.Context, appwire.Message) appwire.Message
}, id int64, params appwire.TurnCancelQueuedParams) appwire.Message {
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	return conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(id), appwire.MethodTurnCancelQueued, params))
}

func TestServerAppWireTurnCancelQueuedDispatchesIndex(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	gotIndex := -1
	var gotExpectedID string
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Cancel: func(params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			gotIndex = params.Index
			gotExpectedID = params.ExpectedEntryID
			return appwire.TurnCancelQueuedResponse{RemovedText: "the removed text", RemovedImages: 2}, nil
		},
	})

	conn := srv.AppServer().NewConnection("test")
	resp := cancelRPC(conn, 2, appwire.TurnCancelQueuedParams{ClientMutationID: "test-mutation", Ref: "local:th_1", Index: 1, ExpectedEntryID: "q_1_abc"})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if gotIndex != 1 || gotExpectedID != "q_1_abc" {
		t.Fatalf("cancel func got index=%d expectedID=%q, want 1/q_1_abc", gotIndex, gotExpectedID)
	}
	got, ok := resp.Response.Result.(appwire.TurnCancelQueuedResponse)
	if !ok {
		t.Fatalf("result type=%T, want TurnCancelQueuedResponse", resp.Response.Result)
	}
	if got.RemovedText != "the removed text" || got.RemovedImages != 2 {
		t.Fatalf("response=%+v, want removedText/removedImages echoed", got)
	}
}

// TestServerAppWireTurnCancelQueuedAllowedWhileIdle pins the difference from
// promote: canceling a queued follow-up does not require an in-flight turn.
func TestServerAppWireTurnCancelQueuedAllowedWhileIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle"})
	called := 0
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Cancel: func(appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			called++
			return appwire.TurnCancelQueuedResponse{RemovedText: "text"}, nil
		},
	})

	conn := srv.AppServer().NewConnection("test")
	resp := cancelRPC(conn, 2, appwire.TurnCancelQueuedParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: 0})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("idle cancel: resp=%v error=%+v, want success", resp.Kind(), resp.Error)
	}
	if called != 1 {
		t.Fatalf("cancel func called %d times, want 1", called)
	}
}

func TestServerAppWireTurnCancelQueuedRejectsNegativeIndex(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Cancel: func(appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			return appwire.TurnCancelQueuedResponse{}, nil
		},
	})

	conn := srv.AppServer().NewConnection("test")
	resp := cancelRPC(conn, 2, appwire.TurnCancelQueuedParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: -1})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("error=%+v, want InvalidParams", resp.Error.Error)
	}
}

func TestServerAppWireTurnCancelQueuedDelegatesWhenProjectionIsClosed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "closed"})
	called := 0
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Cancel: func(appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			called++
			return appwire.TurnCancelQueuedResponse{RemovedText: "stored"}, nil
		},
	})

	conn := srv.AppServer().NewConnection("test")
	resp := cancelRPC(conn, 2, appwire.TurnCancelQueuedParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: 0})
	if resp.Kind() != appwire.MessageResponse || called != 1 {
		t.Fatalf("closed projection dispatch: resp=%v error=%+v called=%d", resp.Kind(), resp.Error, called)
	}
}

func TestServerAppWireTurnCancelQueuedUnavailableWithoutFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})

	conn := srv.AppServer().NewConnection("test")
	resp := cancelRPC(conn, 2, appwire.TurnCancelQueuedParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: 0})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeUnavailable {
		t.Fatalf("error=%+v, want Unavailable", resp.Error.Error)
	}
}

// TestServerAppWireTurnCancelQueuedPropagatesSessionError pins the review-F2
// mapping: session-side rejections (stale index, shifted id) are Conflicts
// so the client can re-sync its preview.
func TestServerAppWireTurnCancelQueuedPropagatesSessionError(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Cancel: func(appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			return appwire.TurnCancelQueuedResponse{}, appwire.Conflict("cancel: queue index 3 out of range (depth 1)")
		},
	})

	conn := srv.AppServer().NewConnection("test")
	resp := cancelRPC(conn, 2, appwire.TurnCancelQueuedParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: 3})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v, want Conflict", resp.Error.Error)
	}
}

// TestServerAppWireTurnCancelQueuedThroughSession exercises the full stack
// the way cmd/serf serve wires it: the RPC drives the real agent session's
// CancelQueued, so the canceled entry leaves the queue (and is never
// consumed) while the other queued message stays queued. The thread
// snapshot carries the full Texts the edit affordance restores into the
// composer.
func TestServerAppWireTurnCancelQueuedThroughSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &blockingServerAdapter{name: "openai", started: make(chan struct{}), done: make(chan error, 1)}
	c.Register(adapter)
	sess, err := agent.NewSession(c, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = sess.ProcessInput(ctx, "keep turn active", nil)
	}()
	select {
	case <-adapter.started:
	case <-time.After(time.Second):
		t.Fatal("session did not enter active turn")
	}

	for _, msg := range []string{"alpha\nwith a second line", "bravo"} {
		if err := sess.Enqueue(context.Background(), msg); err != nil {
			t.Fatalf("Enqueue %q: %v", msg, err)
		}
	}

	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: sess.ID(), State: "active"})
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Cancel: sess.AcceptClientMutationCancelQueued,
	})
	publishSessionQueueEnvelope(srv, sess)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))

	// The thread snapshot must carry the full untruncated texts and the
	// entry ids the UI sends back as expected identity.
	readResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(9), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:" + sess.ID()}))
	if readResp.Kind() != appwire.MessageResponse {
		t.Fatalf("thread/read: %v", readResp.Kind())
	}
	read, ok := readResp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read result type=%T", readResp.Response.Result)
	}
	queue := read.Thread.Serf.Queue
	if len(queue.IDs) != 2 || queue.IDs[0] == "" || queue.IDs[1] == "" {
		t.Fatalf("thread queue IDs = %#v, want two non-empty ids", queue.IDs)
	}
	if len(queue.Texts) != 2 || queue.Texts[0] != "alpha\nwith a second line" || queue.Texts[1] != "bravo" {
		t.Fatalf("thread queue Texts = %#v, want full untruncated texts", queue.Texts)
	}

	// Review F1: a cancel naming the WRONG entry id (a stale snapshot) is a
	// Conflict and leaves the queue fully intact — nothing is removed.
	mismatch := cancelRPC(conn, 3, appwire.TurnCancelQueuedParams{ClientMutationID: "cancel-mismatch", Ref: "local:" + sess.ID(), Index: 0, ExpectedEntryID: queue.IDs[1]})
	if mismatch.Kind() != appwire.MessageError {
		t.Fatalf("mismatch cancel: expected error, got %v", mismatch.Kind())
	}
	if mismatch.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("mismatch error=%+v, want Conflict", mismatch.Error.Error)
	}
	if preview := sess.QueuePreview(); len(preview) != 2 {
		t.Fatalf("QueuePreview after mismatch: got %#v, want both entries intact", preview)
	}

	resp := cancelRPC(conn, 2, appwire.TurnCancelQueuedParams{ClientMutationID: "cancel-alpha", Ref: "local:" + sess.ID(), Index: 0, ExpectedEntryID: queue.IDs[0]})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	removed, ok := resp.Response.Result.(appwire.TurnCancelQueuedResponse)
	if !ok {
		t.Fatalf("result type=%T", resp.Response.Result)
	}
	if removed.RemovedText != "alpha\nwith a second line" {
		t.Fatalf("removedText = %q, want the full multi-line message", removed.RemovedText)
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "bravo" {
		t.Fatalf("QueuePreview after cancel: got %#v, want [bravo]", preview)
	}
	cancel()
	select {
	case <-adapter.done:
	case <-time.After(time.Second):
		t.Fatal("session turn did not finish after cancel")
	}
}
