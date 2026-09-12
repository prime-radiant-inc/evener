package appserver

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

type requestAdmissionTestKey struct{}

func TestRequestAdmissionContextSurvivesSerialQueue(t *testing.T) {
	var epoch atomic.Int32
	epoch.Store(1)
	admitted := make(chan struct{})
	observed := make(chan context.Context, 1)
	server := NewServer(ServerConfig{RequestAdmissionContext: func(ctx context.Context, message appwire.Message) context.Context {
		if message.Request.Method != appwire.MethodThreadModelSet {
			return ctx
		}
		ctx = context.WithValue(ctx, requestAdmissionTestKey{}, epoch.Load())
		close(admitted)
		return ctx
	}})
	serialStarted, release := parkThreadList(t, server)
	HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(ctx context.Context, _ appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		observed <- ctx
		return appwire.EmptyResponse{}, nil
	})
	client := dialAppWireClient(t, serveWebSocketHTTP(t, server))
	go func() { _, _ = client.ThreadList(t.Context(), appwire.ThreadListParams{}) }()
	waitFor(t, "serial handler", serialStarted)
	done := make(chan error, 1)
	go func() {
		done <- client.Request(t.Context(), appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: "local:A", Model: "test", ModelProvider: "test"}, nil)
	}()
	waitFor(t, "request admission", admitted)
	epoch.Store(2)
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx := <-observed
	if ctx.Value(requestAdmissionTestKey{}) != int32(1) {
		t.Fatalf("queued request recaptured metadata: %v", ctx.Value(requestAdmissionTestKey{}))
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("admitted context lost connection cancellation")
	}
}

func TestRequestAdmissionFailureKeepsConnectionUsable(t *testing.T) {
	for _, mode := range []string{"panic", "nil context"} {
		t.Run(mode, func(t *testing.T) {
			server := NewServer(ServerConfig{RequestAdmissionContext: func(ctx context.Context, message appwire.Message) context.Context {
				if message.Request.Method != appwire.MethodThreadModelSet {
					return ctx
				}
				if mode == "panic" {
					panic("fixture admission failure")
				}
				return nil
			}})
			client := dialAppWireClient(t, serveWebSocketHTTP(t, server))
			err := client.Request(t.Context(), appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: "local:A", Model: "test", ModelProvider: "test"}, nil)
			if err == nil {
				t.Fatal("failed admission accepted")
			}
			if err := client.Request(t.Context(), appwire.MethodPing, appwire.EmptyParams{}, nil); err != nil {
				t.Fatalf("admission failure broke connection: %v", err)
			}
		})
	}
}

func TestRecoveryConnectionReachesHandlerWhilePrimaryQueueIsFull(t *testing.T) {
	server := NewServer(ServerConfig{})
	blocked := make(chan struct{})
	server.blockedEnqueue = sync.OnceFunc(func() { close(blocked) })
	started, release := parkThreadList(t, server)
	enteredRecovery := make(chan struct{})
	HandleTyped(server.Router(), appwire.MethodEvenerThreadForceStop, func(context.Context, appwire.ThreadForceStopParams) (appwire.EmptyResponse, error) {
		close(enteredRecovery)
		return appwire.EmptyResponse{}, nil
	})
	hub := serveWebSocketHTTP(t, server)
	primary := dialRawAppWire(t, hub)
	initializeRaw(t, primary)
	for id := int64(2); id < 68; id++ {
		sendRaw(t, primary, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	waitFor(t, "stalled primary worker", started)
	waitFor(t, "full 64-entry queue blocking primary receive loop", blocked)
	recovery := dialAppWireClient(t, hub)
	if err := recovery.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "independent recovery handler", enteredRecovery)
	release()
}

func TestConnectionAdmissionPrecedesUnreadBacklog(t *testing.T) {
	var generation atomic.Uint64
	server := NewServer(ServerConfig{ConnectionAdmissionContext: func(ctx context.Context) context.Context {
		return context.WithValue(ctx, requestAdmissionTestKey{}, generation.Load())
	}})
	blocked := make(chan struct{})
	server.blockedEnqueue = sync.OnceFunc(func() { close(blocked) })
	started, release := parkThreadList(t, server)
	var resumed, mutated atomic.Int32
	check := func(ctx context.Context) error {
		if ctx.Value(requestAdmissionTestKey{}).(uint64) != generation.Load() {
			return appwire.Unavailable("session requires explicit Resume on a fresh connection")
		}
		return nil
	}
	HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, _ appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		if err := check(ctx); err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		resumed.Add(1)
		return appwire.ThreadResumeResponse{}, nil
	})
	HandleTyped(server.Router(), appwire.MethodThreadReasoningEffortSet, func(ctx context.Context, _ appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
		if err := check(ctx); err != nil {
			return appwire.EmptyResponse{}, err
		}
		mutated.Add(1)
		return appwire.EmptyResponse{}, nil
	})
	HandleTyped(server.Router(), appwire.MethodEvenerThreadForceStop, func(context.Context, appwire.ThreadForceStopParams) (appwire.EmptyResponse, error) {
		generation.Add(1)
		return appwire.EmptyResponse{}, nil
	})
	hub := serveWebSocketHTTP(t, server)
	primary := dialRawAppWire(t, hub)
	initializeRaw(t, primary)
	for id := int64(2); id < 68; id++ {
		sendRaw(t, primary, rawRequest(t, id, appwire.MethodThreadList, appwire.ThreadListParams{}))
	}
	waitFor(t, "stalled worker", started)
	waitFor(t, "full queue", blocked)
	sendRaw(t, primary, rawRequest(t, 68, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: "local:owner"}))
	sendRaw(t, primary, rawRequest(t, 69, appwire.MethodThreadReasoningEffortSet, appwire.ThreadReasoningEffortSetParams{Ref: "local:owner", ReasoningEffort: "high"}))
	recovery := dialAppWireClient(t, hub)
	if err := recovery.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}, nil); err != nil {
		t.Fatal(err)
	}
	release()
	for id := int64(2); id <= 69; id++ {
		message, err := primary.Recv(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if id >= 68 && message.Error == nil {
			t.Fatalf("unread request %d admitted after recovery: %+v", id, message)
		}
	}
	if resumed.Load() != 0 || mutated.Load() != 0 {
		t.Fatal("unread actions replayed")
	}
	fresh := dialAppWireClient(t, hub)
	if err := fresh.Request(t.Context(), appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: "local:owner"}, nil); err != nil {
		t.Fatal(err)
	}
	if resumed.Load() != 1 {
		t.Fatal("fresh explicit resume failed")
	}
}
