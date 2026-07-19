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
	srv.SetPromoteQueuedAsSteerFunc(func(index int) error {
		called++
		return nil
	})
	return srv, &called
}

func promoteRPC(conn interface {
	HandleMessage(context.Context, appwire.Message) appwire.Message
}, id int64, params appwire.TurnPromoteQueuedAsSteerParams) appwire.Message {
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	return conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(id), appwire.MethodTurnPromoteQueuedAsSteer, params))
}

func TestServerAppWireTurnPromoteQueuedAsSteerDispatchesIndex(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	gotIndex := -1
	srv.SetPromoteQueuedAsSteerFunc(func(index int) error {
		gotIndex = index
		return nil
	})

	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", Index: 1})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if gotIndex != 1 {
		t.Fatalf("promote callback index=%d, want 1", gotIndex)
	}
}

func TestServerAppWireTurnPromoteQueuedAsSteerRejectsNegativeIndex(t *testing.T) {
	srv, called := newPromoteTestServer(t, true)
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", Index: -1})
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
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", Index: 0})
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
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", Index: 0})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeUnavailable {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
}

func TestServerAppWireTurnPromoteQueuedAsSteerPropagatesSessionError(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	srv.SetPromoteQueuedAsSteerFunc(func(index int) error {
		return errors.New("promote: queue index 3 out of range (depth 1)")
	})
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", Index: 3})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
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
	srv.SetPromoteQueuedAsSteerFunc(func(index int) error {
		return sess.PromoteQueuedAsSteer(context.Background(), index)
	})

	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:" + sess.ID(), Index: 0})
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
