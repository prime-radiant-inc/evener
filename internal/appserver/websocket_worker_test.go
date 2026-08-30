package appserver

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// dispatchSlowSerialServer builds a server whose thread/list handler — a
// serial method that rides the request queue — blocks until released, plus an
// http server speaking AppWire over WebSocket. The returned release is
// idempotent and also runs at test cleanup, so a parked handler can never
// outlive the test.
func dispatchSlowSerialServer(t *testing.T) (*Server, *httptest.Server, chan struct{}, func()) {
	t.Helper()
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		select {
		case <-handlerStarted:
		default:
			close(handlerStarted)
		}
		<-releaseHandler
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_serial_held"}}}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	release := func() {
		select {
		case <-releaseHandler:
		default:
			close(releaseHandler)
		}
	}
	t.Cleanup(release)
	return server, httpServer, handlerStarted, release
}

// TestServeWebSocketSlowSerialMutationDoesNotDelayPing pins the capability the
// serial worker adds: with a serial (inline-set) handler parked mid-flight,
// the browser's app-level ping heartbeat still completes on the same
// connection, because ping bypasses the request queue while the parked
// handler occupies only the worker. This is the slow-mutation twin of
// TestServeWebSocketSlowHandlerDoesNotDelayPing and fails against PR #667,
// where every serial handler occupies the receive loop that answers ping.
func TestServeWebSocketSlowSerialMutationDoesNotDelayPing(t *testing.T) {
	_, httpServer, handlerStarted, _ := dispatchSlowSerialServer(t)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		slowDone <- err
	}()
	waitFor(t, "slow serial handler to start", handlerStarted)

	pingDone := make(chan error, 1)
	go func() {
		var out appwire.EmptyResponse
		pingDone <- client.Request(ctx, appwire.MethodPing, appwire.EmptyParams{}, &out)
	}()
	select {
	case err := <-pingDone:
		if err != nil {
			t.Fatalf("ping failed while a serial handler was parked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ping was starved by a parked serial handler")
	}

	select {
	case err := <-slowDone:
		t.Fatalf("parked serial handler completed before it was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestServeWebSocketKeepaliveStaysLiveDuringSlowSerialMutation pins the
// incidental improvement the receive-loop split delivers: because the loop
// returns to Recv immediately after enqueuing a frame, the keepalive read
// gate reports the reader available while a serial handler executes, so
// dead-peer detection keeps running. Fails against PR #667, where the gate
// reports the reader unavailable for every inline handler's whole runtime.
func TestServeWebSocketKeepaliveStaysLiveDuringSlowSerialMutation(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	ticker := newControlledKeepaliveTicker()
	decision := make(chan bool, 2)
	server.keepaliveTickerFactory = func(time.Duration) webSocketKeepaliveTicker { return ticker }
	server.keepaliveDecision = func(ok bool) { decision <- ok }

	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	release := func() {
		select {
		case <-releaseHandler:
		default:
			close(releaseHandler)
		}
	}
	t.Cleanup(release)
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(handlerStarted)
		<-releaseHandler
		return appwire.ThreadListResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	client := dialAppWireClient(t, httpServer)
	ctx := context.Background()

	slowDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		slowDone <- err
	}()
	waitFor(t, "serial handler to start", handlerStarted)

	// The receive loop's return to Recv after enqueuing is not ordered against
	// the handler starting, so the first tick can land inside the loop's brief
	// unavailable window; the property under test is that the gate reports an
	// available reader while the handler stays parked, so drive ticks until a
	// decision says so — under PR #667 every decision is false for as long as
	// the handler is parked, and the deadline fails the test.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ticker.Tick()
		var sawAvailable bool
		select {
		case ok := <-decision:
			sawAvailable = ok
		case <-deadline.C:
			t.Fatal("keepalive never observed an available reader while a serial handler was parked")
		}
		if sawAvailable {
			break
		}
	}

	select {
	case err := <-slowDone:
		t.Fatalf("parked serial handler completed before it was released: %v", err)
	default:
	}
	release()
	if err := waitFor(t, "parked serial handler to complete", slowDone); err != nil {
		t.Fatalf("serial request failed after release: %v", err)
	}
}

// TestServeWebSocketDisconnectCancelsExecutingSerialMutationPromptly pins the
// promptness half of the admitted-request contract: a context-aware serial
// mutation parked at an await point observes connection cancellation promptly
// when the client disconnects, because the receive loop is parked in Recv —
// not occupied by the handler — and sees the close immediately. Fails against
// PR #667, where the loop cannot observe a close while an inline serial
// handler runs.
func TestServeWebSocketDisconnectCancelsExecutingSerialMutationPromptly(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	handlerStarted := make(chan struct{})
	handlerCanceled := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(ctx context.Context, _ appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		close(handlerStarted)
		select {
		case <-ctx.Done():
			close(handlerCanceled)
		case <-time.After(5 * time.Second):
		}
		return appwire.EmptyResponse{}, ctx.Err()
	})
	httpServer := serveWebSocketHTTP(t, server)

	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	go func() {
		_ = client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{Ref: "local:th_1", ModelProvider: "p", Model: "m"})
	}()
	waitFor(t, "serial mutation to start", handlerStarted)

	if err := transport.Close(); err != nil {
		t.Fatalf("close transport: %v", err)
	}
	waitFor(t, "executing mutation's context to cancel on disconnect", handlerCanceled)
}
