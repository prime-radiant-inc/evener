package appserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestServerSubscriptionDebugSnapshotShowsCaptureState(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "debug-test"})
	conn := server.NewConnection("conn-debug")
	server.registerConnection(conn)
	t.Cleanup(func() { server.unregisterConnection(conn) })

	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	ctx = context.WithValue(ctx, requestIDContextKey{}, requestIDKey(appwire.NewIntID(7)))
	if !CaptureSubscription(
		ctx,
		false,
		func() string { return "local:thread-1" },
		func() uint64 { return 0 },
		func() bool { return true },
	) {
		t.Fatal("capture subscription failed")
	}
	server.Broadcast("local:thread-1", "debug/notice", map[string]any{"value": 1})
	if !conn.enqueue(appwire.NotificationMessage("debug/queued", map[string]any{"value": 2})) {
		t.Fatal("queue diagnostic notification")
	}

	method := reflect.ValueOf(server).MethodByName("DebugSubscriptions")
	if !method.IsValid() {
		t.Fatal("Server.DebugSubscriptions is missing")
	}
	values := method.Call(nil)
	if len(values) != 1 {
		t.Fatalf("DebugSubscriptions returned %d values, want 1", len(values))
	}
	raw, err := json.Marshal(values[0].Interface())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var got struct {
		RoutedSequence uint64 `json:"routedSequence"`
		Connections    []struct {
			ConnectionID      string `json:"connectionId"`
			SendQueueDepth    int    `json:"sendQueueDepth"`
			SendQueueCapacity int    `json:"sendQueueCapacity"`
			PendingHydrations []struct {
				ResponseID             string `json:"responseId"`
				ThreadID               string `json:"threadId"`
				Generation             uint64 `json:"generation"`
				Replace                bool   `json:"replace"`
				ReleaseOnErrorResponse bool   `json:"releaseOnErrorResponse"`
			} `json:"pendingHydrations"`
		} `json:"connections"`
		Subscriptions []struct {
			ConnectionID string `json:"connectionId"`
			ThreadID     string `json:"threadId"`
			Buffering    bool   `json:"buffering"`
			Generation   uint64 `json:"generation"`
			Cut          uint64 `json:"cut"`
			Buffered     int    `json:"bufferedFrames"`
			Withdrawn    bool   `json:"withdrawn"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if got.RoutedSequence != 1 {
		t.Fatalf("routedSequence = %d, want 1", got.RoutedSequence)
	}
	if len(got.Connections) != 1 {
		t.Fatalf("connections = %+v, want one", got.Connections)
	}
	connection := got.Connections[0]
	if connection.ConnectionID != "conn-debug" || connection.SendQueueDepth != 1 || connection.SendQueueCapacity != appwire.NotificationBufferCap {
		t.Fatalf("connection = %+v", connection)
	}
	if len(connection.PendingHydrations) != 1 {
		t.Fatalf("pending hydrations = %+v, want one", connection.PendingHydrations)
	}
	pending := connection.PendingHydrations[0]
	if pending.ResponseID != "7" || pending.ThreadID != "local:thread-1" || pending.Generation != 1 || pending.Replace || !pending.ReleaseOnErrorResponse {
		t.Fatalf("pending hydration = %+v", pending)
	}
	if len(got.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %+v, want one", got.Subscriptions)
	}
	subscription := got.Subscriptions[0]
	if subscription.ConnectionID != "conn-debug" || subscription.ThreadID != "local:thread-1" || !subscription.Buffering || subscription.Generation != 1 || subscription.Cut != 0 || subscription.Buffered != 1 || subscription.Withdrawn {
		t.Fatalf("subscription = %+v", subscription)
	}
}
