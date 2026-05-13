package appserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/serf/internal/appwire"
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
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
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

	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	server.Broadcast("th_1", appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "th_1",
		Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusProcessing},
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyThreadStatusChanged {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification")
	}
}
