package appserver

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// afterWriteOrder records the interleaving of transport sends and after-write
// callbacks, so a test can prove the callback runs after the frame reached the
// transport rather than merely eventually.
type afterWriteOrder struct {
	mu     sync.Mutex
	events []string
}

func (o *afterWriteOrder) record(event string) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.mu.Unlock()
}

func (o *afterWriteOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

type orderRecordingSender struct {
	order *afterWriteOrder
}

func (s *orderRecordingSender) Send(context.Context, appwire.Message) error {
	s.order.record("send")
	return nil
}

func TestAfterResponseWrittenRunsAfterTransportSend(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", Version: "1", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	id := appwire.NewIntID(7)
	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	ctx = context.WithValue(ctx, requestIDContextKey{}, requestIDKey(id))

	order := &afterWriteOrder{}
	ran := make(chan struct{})
	if !AfterResponseWritten(ctx, func() {
		order.record("callback")
		close(ran)
	}) {
		t.Fatal("AfterResponseWritten reported no connection on a handler context")
	}

	conn.send <- appwire.ResponseMessage(id, map[string]any{"ok": true})
	go runWebSocketSendLoopWithTimeout(t.Context(), &orderRecordingSender{order: order}, conn.send, time.Second, conn.responseWritten)

	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("after-write callback never ran")
	}
	if got := order.snapshot(); len(got) != 2 || got[0] != "send" || got[1] != "callback" {
		t.Fatalf("event order = %v, want the callback after the transport send", got)
	}
}

func TestAfterResponseWrittenWithoutConnectionReportsFalse(t *testing.T) {
	ran := false
	if AfterResponseWritten(context.Background(), func() { ran = true }) {
		t.Fatal("AfterResponseWritten reported a connection on a context that carries none")
	}
	if ran {
		t.Fatal("callback ran without a connection")
	}
}

func TestRunPendingAfterWriteRunsUnwrittenCallbacks(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", Version: "1", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	ctx = context.WithValue(ctx, requestIDContextKey{}, requestIDKey(appwire.NewIntID(9)))

	calls := 0
	if !AfterResponseWritten(ctx, func() { calls++ }) {
		t.Fatal("AfterResponseWritten reported no connection on a handler context")
	}
	conn.runPendingAfterWrite()
	conn.runPendingAfterWrite()
	if calls != 1 {
		t.Fatalf("callback ran %d times, want exactly one run", calls)
	}
}

func TestAfterResponseWrittenReportsFalseAfterTeardown(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", Version: "1", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	ctx = context.WithValue(ctx, requestIDContextKey{}, requestIDKey(appwire.NewIntID(11)))

	conn.runPendingAfterWrite()

	ran := false
	if AfterResponseWritten(ctx, func() { ran = true }) {
		t.Fatal("AfterResponseWritten accepted a callback after the send loop had drained")
	}
	conn.runPendingAfterWrite()
	if ran {
		t.Fatal("a callback registered after teardown ran")
	}
}
