package appserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
)

func TestServeWebSocketHandlesAppWire(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(ctx)

	if _, err := client.ThreadList(ctx, appwire.ThreadListParams{}); err == nil {
		t.Fatal("ThreadList before initialize succeeded")
	}
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadList(ctx, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "th_1" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestServeWebSocketPingsIdleReaderWhileBusyRPCHandlerRuns(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	ticker := newControlledKeepaliveTicker()
	decision := make(chan bool, 2)
	server.keepaliveTickerFactory = func(time.Duration) webSocketKeepaliveTicker { return ticker }
	server.keepaliveDecision = func(ok bool) { decision <- ok }

	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHandler) }) }
	defer release()
	HandleTyped(server.Router(), appwire.MethodThreadList, func(ctx context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(handlerStarted)
		<-releaseHandler
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_held"}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	idlePingSeen := make(chan struct{}, 1)
	ctx := context.Background()
	wsConn, _, err := websocket.Dial(ctx, "ws"+httpServer.URL[len("http"):], &websocket.DialOptions{
		HTTPClient: httpServer.Client(),
		OnPingReceived: func(context.Context, []byte) bool {
			select {
			case idlePingSeen <- struct{}{}:
			default:
			}
			return true
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	transport := appwire.NewWSTransport(wsConn)
	client := appwire.NewClient(transport)
	client.Start(ctx)
	defer transport.Close() //nolint:errcheck // test cleanup

	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(ctx, appwire.ThreadListParams{})
		result <- err
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("ordinary RPC handler did not become ready")
	}

	// Drive the actual ServeWebSocket keepalive goroutine while HandleMessage is
	// held busy in a handler goroutine. Concurrent dispatch keeps the receive
	// loop reading, so the gate observes an available reader and the keepalive
	// pings even while the RPC handler is still held. The decision is emitted
	// only after the gate has been consulted, so release is not time-based.
	ticker.Tick()
	select {
	case attempted := <-decision:
		if !attempted {
			t.Fatal("keepalive did not attempt a ping while the receive loop was free and the RPC handler was busy")
		}
		release()
	case <-idlePingSeen:
		release()
	case <-time.After(time.Second):
		t.Fatal("keepalive did not report the available-reader decision")
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("held RPC failed after exact release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("held RPC did not complete after exact release")
	}
	// Receiving the RPC response does not order the send goroutine against the
	// receive loop's next readerAvailable transition, so drive controlled ticks
	// until the keepalive observes that transition and pings the idle peer.
	//
	// Under concurrent dispatch the ping is what proves the fix this test was
	// renamed around: the heartbeat answers while the held handler is busy.
	sawAvailablePing := false
	availableDeadline := time.NewTimer(time.Second)
	defer availableDeadline.Stop()
	for !sawAvailablePing {
		ticker.Tick()
		select {
		case attempted := <-decision:
			if attempted {
				sawAvailablePing = true
			}
		case <-availableDeadline.C:
			t.Fatal("keepalive did not observe the available reader")
		}
	}

	select {
	case <-idlePingSeen:
	case <-time.After(time.Second):
		t.Fatal("idle keepalive never attempted a native ping")
	}
}

func TestServeWebSocketPushesNotificationsToSubscribedThread(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		Subscribe(ctx, "th_1")
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1"}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(ctx)

	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	server.Broadcast("th_1", appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "th_1",
		Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyThreadStatusChanged {
			t.Fatalf("method=%q", got.Method)
		}
		var params appwire.ThreadStatusChangedParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal params: %v", err)
		}
		if params.ThreadID != "th_1" {
			t.Fatalf("params.ThreadID=%q, want %q", params.ThreadID, "th_1")
		}
		if params.Status.Type != appwire.ThreadStatusActive {
			t.Fatalf("params.Status.Type=%q, want %q", params.Status.Type, appwire.ThreadStatusActive)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification")
	}
}
