package appserver

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// BroadcastAll is not a hydration fence. Hold the post-response/pre-release
// boundary and verify that it overtakes a buffered Broadcast without losing it.
func TestHydrationBroadcastAllCanOvertakeBufferedNotification(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "hydration-barrier", SourceID: "local"})
	conn := server.NewConnection("hydration-barrier")
	server.registerConnection(conn)
	t.Cleanup(func() { server.unregisterConnection(conn) })
	deadline, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ctx := context.WithValue(deadline, connectionContextKey{}, conn)
	ctx = context.WithValue(ctx, requestIDContextKey{}, "1")
	if !CaptureSubscriptionWithHandoff(ctx, true, func() string { return "thread" }, func() uint64 { return 0 }, func() bool { return true }, CaptureSubscriptionHandoff{}) {
		t.Fatal("capture rejected")
	}
	server.Broadcast("thread", "test/delta", map[string]any{"id": "during-read"})
	queued, permit := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(permit) })
	server.beforeHydrationCommit = func() { close(queued); <-permit }
	enqueued := make(chan error, 1)
	go func() {
		enqueued <- conn.enqueueResponse(deadline, appwire.ResponseMessage(appwire.NewIntID(1), struct{}{}))
	}()
	joined := false
	t.Cleanup(func() {
		unblock()
		if !joined {
			select {
			case <-enqueued:
			case <-time.After(time.Second):
				t.Error("response enqueue did not stop after cleanup released hydration")
			}
		}
	})
	select {
	case <-queued:
	case <-deadline.Done():
		t.Fatal("response did not reach pre-release hook")
	}
	nextFrame := func() appwire.Message {
		t.Helper()
		select {
		case frame := <-conn.send:
			return frame
		case <-deadline.Done():
			t.Fatal("timed out waiting for emitted frame")
			return appwire.Message{}
		}
	}
	response := nextFrame()
	if response.Response == nil {
		t.Fatalf("first frame = %+v, want read response", response)
	}
	// Equivalent to observing the replacement RPC response and the relay's
	// upstream acknowledgement, while replay is still awaiting its owner.
	server.BroadcastAll("test/barrier", struct{}{})
	unblock()
	select {
	case err := <-enqueued:
		joined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-deadline.Done():
		t.Fatal("response did not finish hydration release")
	}
	got := []string{"response"}
	for range 2 {
		frame := nextFrame()
		if frame.Notification == nil {
			t.Fatalf("unexpected frame: %+v", frame)
		}
		got = append(got, frame.Notification.Method)
	}
	want := []string{"response", "test/barrier", "test/delta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actual emitted frames = %v, want %v", got, want)
	}
	select {
	case frame := <-conn.send:
		t.Fatalf("unexpected extra frame after replay: %+v", frame)
	default:
	}
}
