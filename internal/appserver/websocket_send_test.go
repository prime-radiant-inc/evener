package appserver

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
)

type blockingSendTransport struct {
	sendStarted chan struct{}
	sendDone    chan struct{}
}

func newBlockingSendTransport() *blockingSendTransport {
	return &blockingSendTransport{
		sendStarted: make(chan struct{}),
		sendDone:    make(chan struct{}),
	}
}

func (t *blockingSendTransport) Send(ctx context.Context, _ appwire.Message) error {
	close(t.sendStarted)
	<-ctx.Done()
	close(t.sendDone)
	return ctx.Err()
}

type deadlineCapturingTransport struct {
	deadline chan bool
}

func (t *deadlineCapturingTransport) Send(ctx context.Context, _ appwire.Message) error {
	_, ok := ctx.Deadline()
	t.deadline <- ok
	return nil
}

func TestWebSocketSendLoopCancelsBlockedWriteWithConnectionContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	send := make(chan appwire.Message, 1)
	transport := newBlockingSendTransport()
	done := make(chan struct{})

	go func() {
		defer close(done)
		runWebSocketSendLoop(ctx, transport, send)
	}()
	send <- appwire.NotificationMessage("thread/status/changed", map[string]any{"threadId": "th_1"})

	select {
	case <-transport.sendStarted:
	case <-time.After(time.Second):
		t.Fatal("send did not start")
	}

	cancel()

	select {
	case <-transport.sendDone:
	case <-time.After(time.Second):
		t.Fatal("blocked send did not observe connection context cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("send loop did not exit after blocked write was canceled")
	}
}

func TestWebSocketSendLoopBoundsEachWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	send := make(chan appwire.Message, 1)
	transport := &deadlineCapturingTransport{deadline: make(chan bool, 1)}
	done := make(chan struct{})

	go func() {
		defer close(done)
		runWebSocketSendLoop(ctx, transport, send)
	}()
	send <- appwire.NotificationMessage("thread/status/changed", map[string]any{"threadId": "th_1"})

	select {
	case ok := <-transport.deadline:
		if !ok {
			t.Fatal("send context did not include a deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("send did not start")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("send loop did not exit after context cancellation")
	}
}
