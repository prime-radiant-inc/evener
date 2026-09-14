package appwire

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"
)

// errStreamFrameTooLarge is returned when a peer sends a frame larger than the
// transport's read limit. One value, returned from both the read and write
// paths, so callers and tests can match it with errors.Is.
var errStreamFrameTooLarge = errors.New("appwire stream: frame exceeds read limit")

// streamFrameLimit caps a single framed message. It is a var (not a const) so
// tests can lower it; production uses the same backstop as the WebSocket
// transport.
var streamFrameLimit = appWireWebSocketReadLimit

// StreamTransport carries AppWire Messages over any byte stream as
// newline-delimited JSON: Send writes one marshaled Message plus '\n', Recv
// reads through the next '\n'. JSON string escaping means a marshaled Message
// never contains a raw newline, so the framing is unambiguous.
//
// This is the spike for the SSH stdio transport: it lets a controller speak
// AppWire to a remote hub over an io.ReadWriteCloser (a spawned `ssh` process's
// stdin/stdout) instead of a WebSocket.
//
// It does not implement Pinger, so the client's keepalive loop skips it. A
// blocked Recv is not cancelable through ctx; the only way to unblock it is
// Close, which closes the underlying ReadWriteCloser.
type StreamTransport struct {
	rw  io.ReadWriteCloser
	sc  *bufio.Scanner
	wmu sync.Mutex
}

func NewStreamTransport(rw io.ReadWriteCloser) *StreamTransport {
	sc := bufio.NewScanner(rw)
	// A hard cap on the scanner's buffer makes an oversized frame fail with
	// bufio.ErrTooLong as the buffer reaches the limit, rather than buffering
	// the whole frame before a post-read length check. The initial buffer is
	// sized from the limit so a lowered limit (tests) is not raised back up by
	// a larger initial capacity.
	sc.Buffer(make([]byte, 0, min(4096, streamFrameLimit)), streamFrameLimit)
	return &StreamTransport{rw: rw, sc: sc}
}

func (t *StreamTransport) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := marshalWSMessage(msg)
	if err != nil {
		return err
	}
	if len(data) > streamFrameLimit {
		return errStreamFrameTooLarge
	}
	// Reserve the newline slot up front so the append cannot reallocate and
	// copy the whole frame on the hot path.
	buf := make([]byte, 0, len(data)+1)
	buf = append(buf, data...)
	buf = append(buf, '\n')
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err = t.rw.Write(buf)
	return err
}

func (t *StreamTransport) Recv(ctx context.Context) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	if !t.sc.Scan() {
		if err := t.sc.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				return Message{}, errStreamFrameTooLarge
			}
			return Message{}, err
		}
		return Message{}, io.EOF
	}
	var msg Message
	if err := unmarshalWSMessage(t.sc.Bytes(), &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

func (t *StreamTransport) Close() error { return t.rw.Close() }
