package appwire

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

// silentPingerTransport is a pingingTransport whose Ping blocks until its
// context is cancelled and then reports the context error — the shape a hub
// subprocess starved past the pong deadline presents (issue #154): the ping
// fails with no transport-level error to distinguish it from a dead socket.
type silentPingerTransport struct {
	*pingingTransport
}

func (s *silentPingerTransport) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestClientKeepaliveLogsTearDown pins the #154 diagnostic: when the client
// keepalive closes the connection it must emit one self-describing log line
// naming the keepalive and the ping error, delivered through the sink
// installed by SetLogf. The socket then presents a normal-looking failure
// ("use of closed network connection" on every later write), so this line is
// the only artifact that separates a pong-timeout teardown under load from a
// genuinely dead hub.
//
// It also pins #783: the line must never reach the standard log package's
// writer. That writer defaults to stderr, and a -debug TUI session (no
// alternate screen) renders stderr straight into bubbletea's live grid,
// corrupting the render permanently — proven in the #779/#782 investigation.
func TestClientKeepaliveLogsTearDown(t *testing.T) {
	var globalLog bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&globalLog)
	t.Cleanup(func() { log.SetOutput(prev) })

	var sink bytes.Buffer
	base := newPingingTransport()
	transport := &silentPingerTransport{base}
	client := NewClient(transport)
	client.SetLogf(func(format string, args ...any) {
		fmt.Fprintf(&sink, format, args...)
	})

	ctx := t.Context()
	client.startWithKeepalive(ctx, time.Millisecond, 5*time.Millisecond)

	select {
	case _, ok := <-client.Notifications():
		if ok {
			t.Fatal("expected notifications channel to close, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive did not tear down the client")
	}

	// The log line is written before closeFn runs, and the teardown observed
	// above is what closeFn caused, so the line is already in the buffer.
	if !strings.Contains(sink.String(), "keepalive") || !strings.Contains(sink.String(), "ping") {
		t.Fatalf("keepalive teardown logged no self-describing line to the injected sink; got:\n%s", sink.String())
	}
	if globalLog.Len() != 0 {
		t.Fatalf("keepalive teardown wrote to the standard log package's writer (would corrupt a live TUI render, issue #783); got:\n%s", globalLog.String())
	}
}

// TestClientKeepaliveDefaultLogfDiscards proves a Client with no SetLogf call
// neither panics nor writes anywhere on keepalive teardown. appwire.Client
// runs inside interactive TUI sessions where the standard log package's
// default stderr destination is unsafe (issue #783), so silence has to be
// what a caller gets for free without wiring a sink — see SetLogf.
func TestClientKeepaliveDefaultLogfDiscards(t *testing.T) {
	var globalLog bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&globalLog)
	t.Cleanup(func() { log.SetOutput(prev) })

	base := newPingingTransport()
	transport := &silentPingerTransport{base}
	client := NewClient(transport)

	ctx := t.Context()
	client.startWithKeepalive(ctx, time.Millisecond, 5*time.Millisecond)

	select {
	case _, ok := <-client.Notifications():
		if ok {
			t.Fatal("expected notifications channel to close, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive did not tear down the client")
	}

	if globalLog.Len() != 0 {
		t.Fatalf("keepalive teardown wrote to the standard log package's writer with no sink configured; got:\n%s", globalLog.String())
	}
}
