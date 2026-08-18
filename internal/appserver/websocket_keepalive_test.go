package appserver

import (
	"context"
	"errors"
	"strings"
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
	ctx := t.Context()
	canceled := make(chan struct{})
	pinger := &scriptedPinger{failFrom: 1}

	go runWebSocketKeepalive(ctx, pinger, func(error) { close(canceled) }, time.Millisecond, 50*time.Millisecond)

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

	go runWebSocketKeepalive(ctx, pinger, func(error) { close(canceled) }, time.Millisecond, 50*time.Millisecond)

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
		runWebSocketKeepalive(ctx, pinger, func(error) {}, time.Millisecond, 50*time.Millisecond)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not return after context cancellation")
	}
}

// TestWebSocketKeepaliveCancelsWithNamedCause pins the kata 18p0 ruling: a
// keepalive-initiated teardown must name itself, because the generic
// "context canceled" it used to surface as cost a full investigation to
// trace back to this mechanism.
func TestWebSocketKeepaliveCancelsWithNamedCause(t *testing.T) {
	ctx := t.Context()
	canceled := make(chan struct{})
	var cause atomic.Value
	pinger := &scriptedPinger{failFrom: 1}

	go runWebSocketKeepalive(ctx, pinger, func(err error) { cause.Store(err); close(canceled) }, time.Millisecond, 50*time.Millisecond)

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not cancel the connection after a failed ping")
	}
	err, _ := cause.Load().(error)
	if err == nil {
		t.Fatal("keepalive canceled without a cause")
	}
	for _, want := range []string{"keepalive", "50ms"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("keepalive cancel cause %q does not mention %q", err, want)
		}
	}
}

// TestWSCloseReasonPrefersCancelCause pins the other half of the same ruling:
// the close frame the peer sees carries the cancel cause when one was set,
// and the receive error itself otherwise.
func TestWSCloseReasonPrefersCancelCause(t *testing.T) {
	recvErr := errors.New("failed to read: context canceled")

	plain, plainCancel := context.WithCancel(context.Background())
	plainCancel()
	if got := wsCloseReason(plain, recvErr); got != recvErr.Error() {
		t.Fatalf("plain cancellation produced close reason %q, want the receive error", got)
	}

	named, namedCancel := context.WithCancelCause(context.Background())
	namedCancel(errors.New("appserver: keepalive ping went unanswered for 10s; closing the connection"))
	got := wsCloseReason(named, recvErr)
	if !strings.Contains(got, "keepalive") {
		t.Fatalf("named cancellation produced close reason %q, want it to carry the keepalive cause", got)
	}
}
