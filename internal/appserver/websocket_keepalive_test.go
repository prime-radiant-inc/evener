package appserver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type controlledKeepaliveTicker struct {
	c       chan time.Time
	stopped chan struct{}
}

func newControlledKeepaliveTicker() *controlledKeepaliveTicker {
	return &controlledKeepaliveTicker{c: make(chan time.Time), stopped: make(chan struct{})}
}

func (t *controlledKeepaliveTicker) Chan() <-chan time.Time { return t.c }

func (t *controlledKeepaliveTicker) Stop() {
	select {
	case <-t.stopped:
	default:
		close(t.stopped)
	}
}

func (t *controlledKeepaliveTicker) Tick() {
	select {
	case t.c <- time.Time{}:
	case <-t.stopped:
		panic("controlled keepalive ticker stopped")
	}
}

type scriptedPinger struct {
	calls    atomic.Int64
	failFrom int64 // ping number (1-based) at and after which Ping returns an error
	started  chan int64
}

func (p *scriptedPinger) Ping(ctx context.Context) error {
	n := p.calls.Add(1)
	if p.started != nil {
		select {
		case p.started <- n:
		default:
		}
	}
	if p.failFrom > 0 && n >= p.failFrom {
		return errors.New("ping failed")
	}
	return nil
}

type deferrablePinger struct {
	calls   atomic.Int64
	started chan int64
}

func (p *deferrablePinger) Ping(ctx context.Context) error {
	n := p.calls.Add(1)
	p.started <- n
	if n == 1 {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestWebSocketKeepaliveCancelsConnectionWhenPingFails(t *testing.T) {
	ctx := t.Context()
	canceled := make(chan struct{})
	pinger := &scriptedPinger{failFrom: 1}

	go runWebSocketKeepalive(ctx, pinger, func() { close(canceled) }, newWebSocketReadGate(), time.Millisecond, 50*time.Millisecond)

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not cancel the connection after a failed ping")
	}
}

func TestWebSocketKeepaliveSurvivesHealthyPings(t *testing.T) {
	ctx := t.Context()
	canceled := make(chan struct{})
	pinger := &scriptedPinger{failFrom: 0} // never fails

	go runWebSocketKeepalive(ctx, pinger, func() { close(canceled) }, newWebSocketReadGate(), time.Millisecond, 50*time.Millisecond)

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
		runWebSocketKeepalive(ctx, pinger, func() {}, newWebSocketReadGate(), time.Millisecond, 50*time.Millisecond)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not return after context cancellation")
	}
}

func TestWebSocketKeepaliveDefersActivePingWhileReaderIsUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		gate := newWebSocketReadGate()
		pinger := &deferrablePinger{started: make(chan int64, 2)}
		canceled := make(chan struct{}, 1)
		ticker := newControlledKeepaliveTicker()

		go runWebSocketKeepaliveWithTicker(ctx, pinger, func() { canceled <- struct{}{} }, gate, time.Second, ticker, nil)
		ticker.Tick()
		if got := <-pinger.started; got != 1 {
			t.Fatalf("first ping = %d, want 1", got)
		}

		gate.readerUnavailable()
		// Let the bounded attempt expire while the receive loop cannot read
		// its pong. Backpressure must not be mistaken for a dead peer.
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-canceled:
			t.Fatal("deferred ping canceled the connection")
		default:
		}

		gate.readerAvailable()
		ticker.Tick()
		if got := <-pinger.started; got != 2 {
			t.Fatalf("second ping = %d, want 2", got)
		}
		synctest.Wait()
		select {
		case <-canceled:
			t.Fatal("keepalive canceled the connection after reader recovery")
		default:
		}
	})
}

func TestWebSocketKeepaliveSkipsPingsWhileReaderIsUnavailable(t *testing.T) {
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	gate := newWebSocketReadGate()
	gate.readerUnavailable()
	pinger := &scriptedPinger{started: make(chan int64, 1)}
	ticker := newControlledKeepaliveTicker()
	decision := make(chan bool, 2)
	done := make(chan struct{})

	go func() {
		defer close(done)
		runWebSocketKeepaliveWithTicker(ctx, pinger, func() {}, gate, time.Second, ticker, func(ok bool) {
			decision <- ok
		})
	}()
	ticker.Tick()
	select {
	case got := <-decision:
		if got {
			t.Fatal("keepalive attempted a ping while reader was unavailable")
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive did not observe the controlled tick")
	}
	select {
	case got := <-pinger.started:
		t.Fatalf("ping %d started while reader was unavailable", got)
	default:
	}

	gate.readerAvailable()
	ticker.Tick()
	select {
	case got := <-decision:
		if !got {
			t.Fatal("keepalive did not observe reader availability")
		}
		select {
		case ping := <-pinger.started:
			if ping != 1 {
				t.Fatalf("resumed ping = %d, want 1", ping)
			}
		case <-time.After(time.Second):
			t.Fatal("ping did not resume after reader became available")
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive did not observe reader availability")
	}
	stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not stop after cleanup")
	}
}
