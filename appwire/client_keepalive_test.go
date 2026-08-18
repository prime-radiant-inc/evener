package appwire

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pingingTransport implements Transport + Pinger. Ping fails once armed, and
// Close unblocks Recv with an error — modeling a real WS transport, where
// closing the conn makes the read return.
type pingingTransport struct {
	reads     chan Message
	closeCh   chan struct{}
	pingFails atomic.Bool
	closed    atomic.Bool
}

func newPingingTransport() *pingingTransport {
	return &pingingTransport{reads: make(chan Message, 8), closeCh: make(chan struct{})}
}

func (p *pingingTransport) Send(context.Context, Message) error {
	// A real WS transport fails every write after Close with exactly this
	// text (github.com/coder/websocket); post-close sends must model it so
	// the keepalive tests exercise the same failure shape kata 18p0 traced.
	if p.closed.Load() {
		return errors.New("failed to write msg: use of closed network connection")
	}
	return nil
}

func (p *pingingTransport) Recv(ctx context.Context) (Message, error) {
	select {
	case msg := <-p.reads:
		return msg, nil
	case <-p.closeCh:
		return Message{}, errors.New("transport closed")
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

func (p *pingingTransport) Ping(ctx context.Context) error {
	if p.pingFails.Load() {
		return errors.New("ping failed")
	}
	return nil
}

func (p *pingingTransport) Close() error {
	if p.closed.CompareAndSwap(false, true) {
		close(p.closeCh)
	}
	return nil
}

func TestClientKeepaliveTearsDownOnPingFailure(t *testing.T) {
	transport := newPingingTransport()
	client := NewClient(transport)

	ctx := t.Context()
	client.startWithKeepalive(ctx, time.Millisecond, 50*time.Millisecond)

	transport.pingFails.Store(true)

	select {
	case _, ok := <-client.Notifications():
		if ok {
			t.Fatal("expected notifications channel to close, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive did not tear down the client after a failed ping")
	}
	if !transport.closed.Load() {
		t.Fatal("keepalive did not close the transport after a failed ping")
	}
}

// blockingPingerTransport is a pingingTransport whose Ping blocks until the
// ping context is cancelled, simulating a peer that never responds to pings.
// This exercises the pong-timeout branch of runClientKeepalive.
type blockingPingerTransport struct {
	*pingingTransport
}

func (b *blockingPingerTransport) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestClientKeepaliveTearsDownOnPongTimeout(t *testing.T) {
	base := newPingingTransport()
	transport := &blockingPingerTransport{base}
	client := NewClient(transport)

	ctx := t.Context()
	// 1 ms interval, 5 ms pong timeout: the blocking Ping will always exceed the
	// timeout, so runClientKeepalive must close the transport. The outer 1 s
	// deadline provides ample headroom under scheduler pressure.
	client.startWithKeepalive(ctx, time.Millisecond, 5*time.Millisecond)

	select {
	case _, ok := <-client.Notifications():
		if ok {
			t.Fatal("expected notifications channel to close, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive did not tear down the client after pong timeout")
	}
	if !transport.closed.Load() {
		t.Fatal("keepalive did not close the transport after pong timeout")
	}
}

// TestClientKeepaliveCloseNamesItself pins the kata 18p0 ruling: when the
// keepalive tears the transport down, every failure a caller sees afterwards
// must say so. The bare "use of closed network connection" this replaces cost
// a full investigation to trace back to this mechanism.
func TestClientKeepaliveCloseNamesItself(t *testing.T) {
	base := newPingingTransport()
	transport := &blockingPingerTransport{base}
	client := NewClient(transport)

	ctx := t.Context()
	client.startWithKeepalive(ctx, time.Millisecond, 5*time.Millisecond)

	select {
	case _, ok := <-client.Notifications():
		if ok {
			t.Fatal("expected notifications channel to close, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive did not tear down the client after pong timeout")
	}

	// The post-close request fails at Send with the transport's own error;
	// the caller-visible text must carry the keepalive's reason too, or the
	// failure reads as an unexplained dead connection.
	err := client.request(ctx, "test/afterClose", nil, nil)
	if err == nil {
		t.Fatal("request on a keepalive-closed client returned nil")
	}
	for _, want := range []string{"keepalive", "5ms", "use of closed network connection"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("post-close request error %q does not mention %q", err, want)
		}
	}
}

func TestClientWithoutPingerHasNoKeepalive(t *testing.T) {
	// memoryTransport is not a Pinger; Start must not spuriously close it.
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx := t.Context()
	client.Start(ctx)

	select {
	case <-client.Notifications():
		t.Fatal("notifications channel closed without any transport error")
	case <-time.After(50 * time.Millisecond):
	}
}
