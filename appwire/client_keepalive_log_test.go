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

// runKeepaliveTearDown starts a client whose every ping is guaranteed to miss
// its pong deadline, waits for the keepalive to tear the connection down, and
// returns whatever that teardown wrote to the standard log package. A nil
// logf leaves SetLogf uncalled, so a caller passing nil exercises the
// untouched default rather than an explicitly installed nil sink.
func runKeepaliveTearDown(t *testing.T, logf func(format string, args ...any)) *bytes.Buffer {
	t.Helper()
	var globalLog bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&globalLog)
	t.Cleanup(func() { log.SetOutput(prev) })

	client := NewClient(&silentPingerTransport{newPingingTransport()})
	if logf != nil {
		client.SetLogf(logf)
	}
	client.startWithKeepalive(t.Context(), time.Millisecond, 5*time.Millisecond)

	select {
	case _, ok := <-client.Notifications():
		if ok {
			t.Fatal("expected notifications channel to close, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive did not tear down the client")
	}
	return &globalLog
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
	var sink bytes.Buffer
	globalLog := runKeepaliveTearDown(t, func(format string, args ...any) {
		fmt.Fprintf(&sink, format, args...)
	})

	// The log line is written before closeFn runs, and the teardown the helper
	// waited for is what closeFn caused, so the line is already in the buffer.
	logged := sink.String()
	if !strings.Contains(logged, "keepalive") || !strings.Contains(logged, "ping") {
		t.Fatalf("keepalive teardown logged no self-describing line to the injected sink; got:\n%s", logged)
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
	globalLog := runKeepaliveTearDown(t, nil)
	if globalLog.Len() != 0 {
		t.Fatalf("keepalive teardown wrote to the standard log package's writer with no sink configured; got:\n%s", globalLog.String())
	}
}
