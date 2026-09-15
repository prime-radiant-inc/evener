package appwire

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"
)

// ErrStreamFrameTooLarge is returned when a frame exceeds the transport's read
// limit, whether the transport is asked to write one or reads one. One value on
// both paths, so callers and tests can match it with errors.Is.
var ErrStreamFrameTooLarge = errors.New("appwire stream: frame exceeds read limit")

// defaultStreamFrameLimit caps a single framed message: the same backstop the
// WebSocket transport uses. Tests that need a smaller cap build a transport with
// NewStreamTransportWithLimit rather than mutating a package-level value.
const defaultStreamFrameLimit = appWireWebSocketReadLimit

// StreamTransport carries AppWire Messages over any byte stream as
// newline-delimited JSON: Send writes one marshaled Message plus '\n', Recv
// reads through the next '\n'. A marshaled Message never contains a raw newline
// — json.Marshal compacts a json.RawMessage's inter-token whitespace and rejects
// a newline inside a string — so the framing is unambiguous.
// TestStreamTransportMarshalNeverEmitsNewline pins that invariant.
//
// This is the spike for the SSH stdio transport: it lets a controller speak
// AppWire to a remote hub over an io.ReadWriteCloser (a spawned `ssh` process's
// stdin/stdout) instead of a WebSocket.
//
// Cancellation: the underlying stream is an io.ReadWriteCloser with no deadline
// API, so a blocked Write or Read cannot be interrupted any other way. While a
// call is in flight a canceled ctx closes the stream to unblock it, and the call
// then returns ctx.Err(). The transport is unusable afterwards, which is the
// deliberate contract: cancellation means teardown, not a retry.
//
// It does not implement Pinger, so the client's keepalive loop skips it.
type StreamTransport struct {
	rw    io.ReadWriteCloser
	br    *bufio.Reader
	limit int
	wmu   sync.Mutex
}

// NewStreamTransport returns a transport with the default frame limit.
func NewStreamTransport(rw io.ReadWriteCloser) *StreamTransport {
	return NewStreamTransportWithLimit(rw, defaultStreamFrameLimit)
}

// NewStreamTransportWithLimit returns a transport whose frames may carry at most
// limit bytes of payload. The reader's chunk buffer stays modest (it grows a
// frame by chunks rather than preallocating the whole limit), but readLine
// enforces limit exactly, so a maximum-size frame is accepted while anything
// larger fails without buffering it all.
func NewStreamTransportWithLimit(rw io.ReadWriteCloser, limit int) *StreamTransport {
	return &StreamTransport{
		rw:    rw,
		br:    bufio.NewReaderSize(rw, min(4096, limit+1)),
		limit: limit,
	}
}

func (t *StreamTransport) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := marshalWSMessage(msg)
	if err != nil {
		return err
	}
	if len(data) > t.limit {
		return ErrStreamFrameTooLarge
	}
	// Reserve the newline slot up front so the append cannot reallocate and
	// copy the whole frame on the hot path.
	buf := make([]byte, 0, len(data)+1)
	buf = append(buf, data...)
	buf = append(buf, '\n')

	t.wmu.Lock()
	defer t.wmu.Unlock()
	// See the cancellation note on StreamTransport: closing is the only way to
	// unblock a Write whose peer has stopped reading.
	stop := context.AfterFunc(ctx, func() { _ = t.rw.Close() })
	defer stop()
	n, err := t.rw.Write(buf)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	if n != len(buf) {
		// A short write leaves a partial frame on the wire; the stream cannot be
		// resynchronized mid-frame, so this is terminal rather than retryable.
		return io.ErrShortWrite
	}
	return nil
}

func (t *StreamTransport) Recv(ctx context.Context) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = t.rw.Close() })
	defer stop()
	line, err := t.readLine()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Message{}, ctxErr
		}
		return Message{}, err
	}
	var msg Message
	if err := unmarshalWSMessage(line, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// readLine reads one newline-terminated frame, bounded by the transport's limit.
// The delimiter is REQUIRED: a stream that ends mid-frame is a torn write, not a
// short message, so it reports io.ErrUnexpectedEOF instead of decoding a
// truncated frame as if it were complete. A clean end of stream is io.EOF.
func (t *StreamTransport) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := t.br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			line = append(line, chunk...)
			if len(line) > t.limit {
				return nil, ErrStreamFrameTooLarge
			}
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) == 0 && len(chunk) == 0 {
					return nil, io.EOF
				}
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		line = append(line, chunk...)
		// The trailing newline is framing, not payload: a frame whose payload is
		// exactly the limit is in bounds.
		if len(line)-1 > t.limit {
			return nil, ErrStreamFrameTooLarge
		}
		return line[:len(line)-1], nil
	}
}

func (t *StreamTransport) Close() error { return t.rw.Close() }
