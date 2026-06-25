package appwire

import (
	"context"
	"errors"
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

func (p *pingingTransport) Send(context.Context, Message) error { return nil }

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

func TestClientWithoutPingerHasNoKeepalive(t *testing.T) {
	// memoryTransport is not a Pinger; Start must not spuriously close it.
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	select {
	case <-client.Notifications():
		t.Fatal("notifications channel closed without any transport error")
	case <-time.After(50 * time.Millisecond):
	}
}
