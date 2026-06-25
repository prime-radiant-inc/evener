package appserver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type scriptedPinger struct {
	calls    atomic.Int64
	failFrom int64 // ping number (1-based) at and after which Ping returns an error
}

func (p *scriptedPinger) Ping(ctx context.Context) error {
	n := p.calls.Add(1)
	if p.failFrom > 0 && n >= p.failFrom {
		return errors.New("ping failed")
	}
	return nil
}

func TestWebSocketKeepaliveCancelsConnectionWhenPingFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan struct{})
	pinger := &scriptedPinger{failFrom: 1}

	go runWebSocketKeepalive(ctx, pinger, func() { close(canceled) }, time.Millisecond, 50*time.Millisecond)

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not cancel the connection after a failed ping")
	}
}

func TestWebSocketKeepaliveSurvivesHealthyPings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan struct{})
	pinger := &scriptedPinger{failFrom: 0} // never fails

	go runWebSocketKeepalive(ctx, pinger, func() { close(canceled) }, time.Millisecond, 50*time.Millisecond)

	select {
	case <-canceled:
		t.Fatal("keepalive canceled a healthy connection")
	case <-time.After(50 * time.Millisecond):
	}
	if pinger.calls.Load() == 0 {
		t.Fatal("keepalive never pinged a healthy connection")
	}
}

func TestWebSocketKeepaliveStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pinger := &scriptedPinger{failFrom: 0}
	done := make(chan struct{})

	go func() {
		defer close(done)
		runWebSocketKeepalive(ctx, pinger, func() {}, time.Millisecond, 50*time.Millisecond)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not return after context cancellation")
	}
}
