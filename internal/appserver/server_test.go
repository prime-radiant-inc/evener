package appserver

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/internal/appwire"
)

func TestConnectionRequiresInitialize(t *testing.T) {
	server := NewServer(ServerConfig{
		ServerName: "serf-hub",
		Version:    "test",
		SourceID:   "local",
	})
	conn := server.NewConnection("conn-1")
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("kind=%v, want error", resp.Kind())
	}
}

func TestConnectionInitializeAllowsLaterRequests(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{}, nil
	})
	conn := server.NewConnection("conn-1")
	initResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if initResp.Kind() != appwire.MessageResponse {
		t.Fatalf("init kind=%v", initResp.Kind())
	}
	listResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if listResp.Kind() != appwire.MessageResponse {
		t.Fatalf("list kind=%v", listResp.Kind())
	}
}

func TestInitializeIsConnectionScoped(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn1 := server.NewConnection("conn-1")
	conn2 := server.NewConnection("conn-2")

	resp := conn1.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("init kind=%v", resp.Kind())
	}
	other := conn2.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if other.Kind() != appwire.MessageError {
		t.Fatalf("other kind=%v, want error", other.Kind())
	}
}

func TestConnectionResponseEnqueueWaitsForCapacity(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	for i := 0; i < cap(conn.send); i++ {
		conn.enqueue(appwire.NotificationMessage("notice", map[string]any{"i": i}))
	}

	response := appwire.ResponseMessage(appwire.NewIntID(42), map[string]string{"ok": "true"})
	done := make(chan error, 1)
	go func() {
		done <- conn.enqueueResponse(context.Background(), response)
	}()

	select {
	case err := <-done:
		t.Fatalf("enqueueResponse returned while buffer was full: %v", err)
	default:
	}

	<-conn.send
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("enqueueResponse: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueueResponse did not complete after capacity became available")
	}

	found := false
	for len(conn.send) > 0 {
		msg := <-conn.send
		if msg.Response != nil && msg.IDString() == "42" {
			found = true
		}
	}
	if !found {
		t.Fatal("response was not delivered after capacity became available")
	}
}

func TestConnectionEnqueueAfterUnregisterDoesNotPanic(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)
	server.unregisterConnection(conn.ID())

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("enqueue after unregister panicked: %v", r)
		}
	}()
	conn.enqueue(appwire.NotificationMessage("notice", nil))
}

func TestServer_BroadcastAll(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn1 := server.NewConnection("conn-1")
	conn2 := server.NewConnection("conn-2")
	server.registerConnection(conn1)
	server.registerConnection(conn2)

	server.BroadcastAll("test/notify", map[string]string{"key": "value"})

	for _, tc := range []struct {
		name string
		conn *Connection
	}{
		{"conn-1", conn1},
		{"conn-2", conn2},
	} {
		select {
		case msg := <-tc.conn.send:
			if msg.Notification == nil {
				t.Fatalf("%s: expected notification, got %+v", tc.name, msg)
			}
			if msg.Notification.Method != "test/notify" {
				t.Fatalf("%s: method=%q, want %q", tc.name, msg.Notification.Method, "test/notify")
			}
		default:
			t.Fatalf("%s: no message received", tc.name)
		}
	}
}

func TestBroadcastDisconnectsSlowSubscriberInsteadOfDroppingNotification(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)
	conn.Subscribe("th_1")
	for i := 0; i < cap(conn.send); i++ {
		conn.enqueue(appwire.NotificationMessage("notice", map[string]any{"i": i}))
	}

	server.Broadcast("th_1", "notice", map[string]any{"overflow": true})

	if got := server.SubscriberCount("th_1"); got != 0 {
		t.Fatalf("subscriber count=%d, want slow subscriber disconnected", got)
	}
	server.mu.RLock()
	_, registered := server.conns[conn.ID()]
	server.mu.RUnlock()
	if registered {
		t.Fatal("slow subscriber connection remained registered after overflow")
	}
}

func TestReplaceSubscriptionsScopesConnectionToLatestThread(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)
	conn.Subscribe("th_old")

	conn.ReplaceSubscriptions("th_new")

	if got := server.SubscriberCount("th_old"); got != 0 {
		t.Fatalf("old subscriber count=%d, want 0", got)
	}
	if got := server.SubscriberCount("th_new"); got != 1 {
		t.Fatalf("new subscriber count=%d, want 1", got)
	}
	if conn.server.subs.IsSubscribed(conn.ID(), "th_old") {
		t.Fatal("connection remained subscribed to old thread")
	}
	if !conn.server.subs.IsSubscribed(conn.ID(), "th_new") {
		t.Fatal("connection was not subscribed to new thread")
	}
}
