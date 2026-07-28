package server

// Tests for the turn/promoteQueuedAsSteer appwire method (issue #22): the
// per-message counterpart of turn/drainAsSteer. The daemon removes the queued
// follow-up at the requested index and injects it as user-sourced steering
// into the in-flight turn; other queued messages stay queued. Failures are
// honest: idle/closed → Conflict, negative index → InvalidParams, no callback
// → Unavailable, and a session-side rejection (e.g. the queue shifted under
// the client so the index is now out of range) propagates.

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func newPromoteTestServer(t *testing.T, processing bool) (*Server, *int) {
	t.Helper()
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(processing)
	state := "idle"
	if processing {
		state = "active"
	}
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: state})
	called := 0
	srv.SetPromoteQueuedAsSteerFunc(func(index int, expectedID string) error {
		called++
		return nil
	})
	return srv, &called
}

func promoteRPC(conn interface {
	HandleMessage(context.Context, appwire.Message) appwire.Message
}, id int64, params appwire.TurnPromoteQueuedAsSteerParams) appwire.Message {
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	return conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(id), appwire.MethodTurnPromoteQueuedAsSteer, params))
}

func TestServerAppWireTurnPromoteQueuedAsSteerDispatchesIndex(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	gotIndex := -1
	var gotExpectedID string
	srv.SetPromoteQueuedAsSteerFunc(func(index int, expectedID string) error {
		gotIndex = index
		gotExpectedID = expectedID
		return nil
	})

	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", Ref: "local:th_1", Index: 1, ExpectedEntryID: "q_1_abc"})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if gotIndex != 1 {
		t.Fatalf("promote callback index=%d, want 1", gotIndex)
	}
	if gotExpectedID != "q_1_abc" {
		t.Fatalf("promote callback expectedID=%q, want q_1_abc", gotExpectedID)
	}
}

func TestServerAppWireTurnPromoteQueuedAsSteerRejectsNegativeIndex(t *testing.T) {
	srv, called := newPromoteTestServer(t, true)
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", ExpectedTurnID: "test-turn", Ref: "local:th_1", Index: -1})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
	if *called != 0 {
		t.Fatalf("promote called=%d, want 0", *called)
	}
}

func TestServerAppWireTurnPromoteQueuedAsSteerRejectsWhenIdle(t *testing.T) {
	srv, called := newPromoteTestServer(t, false)
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", ExpectedTurnID: "test-turn", Ref: "local:th_1", Index: 0})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
	if *called != 0 {
		t.Fatalf("promote called=%d, want 0", *called)
	}
}

func TestServerAppWireTurnPromoteQueuedAsSteerUnavailableWithoutFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", ExpectedTurnID: "test-turn", Ref: "local:th_1", Index: 0})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeUnavailable {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
}

// TestServerAppWireTurnPromoteQueuedAsSteerPropagatesSessionError pins
// review F2: session-side rejections (queue shifted → index out of range or
// expected id mismatch) surface as Conflict so the client can re-sync its
// preview instead of believing the wrong message was promoted.
func TestServerAppWireTurnPromoteQueuedAsSteerPropagatesSessionError(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	srv.SetPromoteQueuedAsSteerFunc(func(index int, expectedID string) error {
		return errors.New("promote: queue index 3 out of range (depth 1)")
	})
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", ExpectedTurnID: "test-turn", Ref: "local:th_1", Index: 3})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v, want Conflict", resp.Error.Error)
	}
}

// TestServerAppWireTurnPromoteQueuedAsSteerThroughSession exercises the full
// stack the way cmd/serf serve wires it: the RPC drives the real agent
// session's PromoteQueuedAsSteer, so the promoted entry leaves the queue and
// lands on the steering queue as user-sourced steering while the other
// queued message stays queued.
func TestServerAppWireTurnPromoteQueuedAsSteerThroughSession(t *testing.T) {
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

	for _, msg := range []string{"alpha", "bravo"} {
		if err := sess.Enqueue(context.Background(), msg); err != nil {
			t.Fatalf("Enqueue %s: %v", msg, err)
		}
	}

	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: sess.ID(), State: "active"})
	srv.SetPromoteQueuedAsSteerFunc(func(index int, expectedID string) error {
		return sess.PromoteQueuedAsSteer(context.Background(), index, expectedID)
	})
	srv.SetQueuePreviewFunc(sess.QueuePreview)
	srv.SetQueueIDsFunc(sess.QueueIDs)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))

	// The thread snapshot must carry the entry ids the UI needs to send back.
	readResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(9), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:" + sess.ID()}))
	if readResp.Kind() != appwire.MessageResponse {
		t.Fatalf("thread/read: %v", readResp.Kind())
	}
	read, ok := readResp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read result type=%T", readResp.Response.Result)
	}
	ids := read.Thread.Serf.Queue.IDs
	if len(ids) != 2 || ids[0] == "" || ids[1] == "" {
		t.Fatalf("thread queue IDs = %#v, want two non-empty ids", ids)
	}

	// Review F1: a promote naming the WRONG entry id (a stale snapshot) is a
	// Conflict and leaves the queue fully intact — nothing is steered.
	mismatch := promoteRPC(conn, 3, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", Ref: "local:" + sess.ID(), Index: 0, ExpectedEntryID: ids[1]})
	if mismatch.Kind() != appwire.MessageError {
		t.Fatalf("mismatch promote: expected error, got %v", mismatch.Kind())
	}
	if mismatch.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("mismatch error=%+v, want Conflict", mismatch.Error.Error)
	}
	if preview := sess.QueuePreview(); len(preview) != 2 {
		t.Fatalf("QueuePreview after mismatch: got %#v, want both entries intact", preview)
	}
	if steering := sess.SteeringQueueSnapshot(); len(steering) != 0 {
		t.Fatalf("steering queue after mismatch: got %+v, want empty", steering)
	}

	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", Ref: "local:" + sess.ID(), Index: 0, ExpectedEntryID: ids[0]})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "bravo" {
		t.Fatalf("QueuePreview after promote: got %#v, want [bravo]", preview)
	}
	steering := sess.SteeringQueueSnapshot()
	if len(steering) != 1 || steering[0].Text != "alpha" {
		t.Fatalf("steering queue after promote: got %+v, want [alpha]", steering)
	}
	cancel()
	select {
	case <-adapter.done:
	case <-time.After(time.Second):
		t.Fatal("active turn did not stop after cancellation")
	}
}
