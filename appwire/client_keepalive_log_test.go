package appwire

import (
	"bytes"
	"context"
	"log"
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
// naming the keepalive and the ping error. The socket then presents a
// normal-looking failure ("use of closed network connection" on every later
// write), so this line is the only artifact that separates a pong-timeout
// teardown under load from a genuinely dead hub.
func TestClientKeepaliveLogsTearDown(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
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

	// The log line is written before closeFn runs, and the teardown observed
	// above is what closeFn caused, so the line is already in the buffer.
	if !bytes.Contains(buf.Bytes(), []byte("keepalive")) || !bytes.Contains(buf.Bytes(), []byte("ping")) {
		t.Fatalf("keepalive teardown logged no self-describing line; got:\n%s", buf.String())
	}
}
