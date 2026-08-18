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
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
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
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Promote: func(appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			called++
			return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
		},
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
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Promote: func(params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			gotIndex = params.Index
			gotExpectedID = params.ExpectedEntryID
			return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
		},
	})

	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", Ref: "local:th_1", Index: 1, ExpectedEntryID: "q_1_abc"})
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
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: -1})
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

func TestServerAppWireTurnPromoteQueuedAsSteerDelegatesWhenProjectionIsIdle(t *testing.T) {
	srv, called := newPromoteTestServer(t, false)
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: 0})
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("idle projection dispatch: resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if *called != 1 {
		t.Fatalf("promote called=%d, want 1", *called)
	}
}

func TestServerAppWireTurnPromoteQueuedAsSteerUnavailableWithoutFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: 0})
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
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Promote: func(appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.Conflict("promote: queue index 3 out of range (depth 1)")
		},
	})
	conn := srv.AppServer().NewConnection("test")
	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedEntryID: "test-entry", Ref: "local:th_1", Index: 3})
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v, want Conflict", resp.Error.Error)
	}
}

// TestServerAppWireTurnPromoteQueuedAsSteerThroughSession exercises the full
// stack the way cmd/evener serve wires it: the RPC drives the real agent
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
	// Started for its side effect: promote needs a turn in flight.
	_, err = sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "promote-active-turn",
		Input:            []appwire.InputItem{{Type: "text", Text: "durable turn"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}

	// Both enqueues must be visible in the DURABLE queue before the envelope is
	// published, or the ids the promote compares against come from a queue that
	// was still filling. Poll for the condition rather than sleeping -- and poll
	// the durable snapshot, since that is the object the promote reads; an
	// earlier version of this barrier polled the mirror and so could not have
	// established what it claimed.
	waitForQueueDepth(t, sess, 2)

	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: sess.ID(), State: "active"})
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Promote: sess.AcceptClientMutationPromoteQueuedAsSteer,
	})
	publishSessionQueueEnvelope(srv, sess)

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
	ids := read.Thread.Evener.Queue.IDs
	if len(ids) != 2 || ids[0] == "" || ids[1] == "" {
		t.Fatalf("thread queue IDs = %#v, want two non-empty ids", ids)
	}

	// Everything below assumes the turn is still parked in the adapter, and
	// nothing used to check it. That assumption is the whole test: promote has
	// no session-state precondition of its own (unlike PromoteQueuedAsSteer,
	// agent/session_queue.go), so a turn that ended early leaves every
	// assertion here passing or failing for reasons that have nothing to do
	// with promote. And the drain loop that runs when a turn ends is the ONLY
	// place in the codebase that takes the queue head away and puts it back:
	// the interrupted-turn recovery in agent/session_lifecycle.go pops, and
	// pushes back when it cannot run what it popped. adapter.done is buffered,
	// so the wait at the end of this test cannot tell an early end from a
	// cancelled-at-the-end one. This can.
	select {
	case err := <-adapter.done:
		t.Fatalf("the turn ended before the promote (%v): the drain loop is now free to claim queue entries, and nothing below measures what it names", err)
	default:
	}

	// The promote is a compare-and-commit against the DURABLE queue as it is
	// when the request lands, so the ids it names must come from that same
	// queue. sess.QueueIDs() is not it: that reads the mirror s.inputQueue,
	// which reflectDurableInputQueue rewrites after the store commit, under a
	// different lock, and skips entirely when the revision has not advanced. So
	// the mirror can still read [alpha bravo] while the durable queue the
	// promote will compare against is already [bravo] -- which is precisely how
	// this test used to report "expected error, got 3" with every other
	// assertion looking intact (kata n1zs). ClientMutationProjection reads the
	// snapshot promote reads.
	durable, _ := sess.ClientMutationProjection()
	if len(durable.IDs) != 2 || durable.IDs[0] != ids[0] || durable.IDs[1] != ids[1] {
		t.Fatalf("the durable queue moved between the read and the promote: durable=%#v published=%#v mirror=%#v; the promote below would compare against a queue this case never described",
			durable.IDs, ids, sess.QueueIDs())
	}

	// Review F1: a promote naming the WRONG entry id (a stale snapshot) is a
	// Conflict and leaves the queue fully intact — nothing is steered.
	mismatch := promoteRPC(conn, 3, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "promote-mismatch", Ref: "local:" + sess.ID(), Index: 0, ExpectedEntryID: durable.IDs[1]})
	if mismatch.Kind() != appwire.MessageError {
		after, _ := sess.ClientMutationProjection()
		t.Fatalf("mismatch promote: expected error, got %v. The promote named index 0 with %q, which is entry 1 of %#v -- for that to be accepted the durable queue had to move under it, and it now reads %#v (mirror %#v)",
			mismatch.Kind(), durable.IDs[1], ids, after.IDs, sess.QueueIDs())
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

	resp := promoteRPC(conn, 2, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "promote-alpha", Ref: "local:" + sess.ID(), Index: 0, ExpectedEntryID: durable.IDs[0]})
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

// waitForQueueDepth blocks until the session's DURABLE queue holds exactly want
// entries. A poll, not a sleep: the condition is what the caller needs, and a
// fixed delay is either too short under load or wasted when it is not.
//
// It reads ClientMutationProjection, not sess.QueueIDs(). QueueIDs reads the
// mirror s.inputQueue (agent/session_queue.go), which reflectDurableInputQueue
// rewrites after the store commit and under a different lock -- so a barrier
// built on it can pass while the durable queue every mutation compares against
// is in a different state, which is the whole failure mode the caller is
// guarding against.
func waitForQueueDepth(t *testing.T, sess *agent.Session, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		durable, _ := sess.ClientMutationProjection()
		got := len(durable.IDs)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("durable queue depth = %d, want %d (mirror reads %#v)", got, want, sess.QueueIDs())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
