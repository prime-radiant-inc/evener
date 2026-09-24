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

// ErrStreamClosed is returned by Send and Recv after Close. The closed state is
// latched rather than left to the underlying stream: bufio.Reader may already
// hold prefetched frames, so a Recv after Close would otherwise still deliver
// messages from a transport the caller has finished with.
var ErrStreamClosed = errors.New("appwire stream: closed")

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
// Failure is terminal once the framing itself breaks. A write that lands only
// part of a frame, or a frame too large to read, leaves bytes on the wire whose
// alignment cannot be recovered — draining an oversize frame would itself be
// unbounded work over attacker-influenced input. The transport poisons itself in
// that case: the stream is closed and every later Send or Recv returns the
// failure that poisoned it, rather than decoding the tail of a rejected frame as
// if it were a message.
//
// It does not implement Pinger, so the client's keepalive loop skips it.
type StreamTransport struct {
	rw    io.ReadWriteCloser
	br    *bufio.Reader
	limit int
	// send is the write lock, a buffered channel rather than a sync.Mutex so a
	// send queued behind another stalled write can still obey its context.
	send chan struct{}
	// opMu admits writes. Close latches under mu and then closes the stream; the
	// read lock makes "check the latch, then write" atomic with respect to that,
	// and the drain on Close is what makes "Close has returned" mean no write can
	// still begin.
	opMu sync.RWMutex

	mu       sync.Mutex
	poisoned error
	// closeOnce runs the underlying close exactly once; every later caller waits
	// for it rather than reaching the closer again, so no caller can observe the
	// stream as closed until it really is.
	closeOnce sync.Once
	closeErr  error
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
	if limit < 1 {
		// The limit is a caller-supplied seam (production goes through
		// NewStreamTransport). A non-positive one is arithmetic nonsense —
		// limit+1 would size the reader buffer at zero — and would leave a
		// transport that silently rejects every frame while looking configured.
		limit = 1
	}
	return &StreamTransport{
		rw:    rw,
		br:    bufio.NewReaderSize(rw, min(4096, limit+1)),
		limit: limit,
		send:  make(chan struct{}, 1),
	}
}

func (t *StreamTransport) Send(ctx context.Context, msg Message) error {
	if err := t.poisonErr(); err != nil {
		return err
	}
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

	// A send queued behind another stalled write must still obey its own
	// context, so the write lock is acquired through a select rather than a
	// plain lock.
	select {
	case t.send <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-t.send }()

	// Re-check under the lock. Another call may have poisoned the transport
	// while this one waited for it — a Recv that read an oversize frame, or a
	// Send that wrote only part of one — and writing now would either return the
	// stream's close error instead of the sticky cause, or, on a stream that
	// tolerates writes after close, push a frame into a desynchronized stream.
	if err := t.poisonErr(); err != nil {
		return err
	}
	// A context canceled while we waited must neither write a frame nor tear the
	// stream down for the call that holds the lock.
	if err := ctx.Err(); err != nil {
		return err
	}
	// Cancellation must be RECORDED before the close it performs is observable.
	// Closing alone let a concurrent call observe the close first and latch the
	// close error, so the transport then reported io.ErrClosedPipe for a teardown
	// the caller had asked for. poison latches under the lock and only then
	// closes, which is the order this needs; and because poison keeps the first
	// cause, a frame that was already broken stays the reported one.
	stop := context.AfterFunc(ctx, func() { t.poison(ctx.Err()) })
	defer stop()

	// Admission, taken atomically with the latch check: a write that starts after
	// Close returned is impossible, and one already in flight is interrupted by
	// Close's own rw.Close() rather than waited for.
	t.opMu.RLock()
	defer t.opMu.RUnlock()
	if err := t.poisonErr(); err != nil {
		return err
	}
	n, err := t.rw.Write(buf)
	if err != nil {
		// Decide terminality from the write itself, before consulting the
		// context: a write that landed only part of a frame has desynchronized
		// the stream whether or not the caller also canceled, and the deferred
		// stop above can cancel the AfterFunc that would otherwise have closed
		// it. Consulting the context first left a desynchronized stream open and
		// let the next Send append a whole frame after the orphaned bytes.
		// Only a write that actually put bytes on the wire is a SHORT write. A
		// zero-byte failure — a broken pipe, a reset, an already-closed stream —
		// is the underlying error, and reporting it as a short write would both
		// discard the real cause and claim part of a frame reached the peer.
		if n > 0 && n != len(buf) {
			t.poison(io.ErrShortWrite)
		} else if ctxErr := ctx.Err(); ctxErr != nil {
			// A zero-byte failure that coincides with cancellation IS the
			// cancellation: the AfterFunc closed the stream under this write.
			// Latching the close error instead would make callers matching
			// context.Canceled miss the teardown they asked for.
			t.poison(ctxErr)
		} else {
			t.poison(err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if pErr := t.poisonErr(); pErr != nil {
			return pErr
		}
		return err
	}
	if n != len(buf) {
		// A short write leaves a partial frame on the wire; the stream cannot be
		// resynchronized mid-frame, so the transport is done. Report whatever
		// actually won the latch — a cancellation or a Close racing this write
		// may have recorded its cause first — so this call's error agrees with
		// the one every later call reports.
		t.poison(io.ErrShortWrite)
		return t.poisonErr()
	}
	// The frame is on the wire, but a cancellation that landed while it was being
	// written has already closed the stream underneath: latch that, so later
	// calls report the cancellation instead of a bare close error.
	if ctxErr := ctx.Err(); ctxErr != nil {
		t.poison(ctxErr)
		return ctxErr
	}
	// stop() reports whether it prevented the callback. If it returns false the
	// cancellation landed in the window since the check above, so the AfterFunc
	// already closed the stream and the cancellation has to be latched even
	// though this frame went out. The deferred stop is an idempotent safety net.
	if err := t.latchLateCancel(ctx, stop); err != nil {
		return err
	}
	// Close may have latched while this frame was being written. Reporting
	// success would tell the caller a closed transport still works.
	if pErr := t.poisonErr(); pErr != nil {
		return pErr
	}
	return nil
}

func (t *StreamTransport) Recv(ctx context.Context) (Message, error) {
	if err := t.poisonErr(); err != nil {
		return Message{}, err
	}
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	// Same ordering as Send: record the cancellation before the close that
	// unblocks this read can be observed by anyone else.
	stop := context.AfterFunc(ctx, func() { t.poison(ctx.Err()) })
	defer stop()
	line, err := t.readLine()
	if err != nil {
		// The recorded cause comes first. readLine poisons a framing break, and
		// the cancel callback poisons the cancellation before performing the
		// close that unblocks this read — so whichever it was is what every later
		// call reports, and this call must not disagree with it by preferring a
		// cancellation that did not actually win.
		if pErr := t.poisonErr(); pErr != nil {
			return Message{}, pErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Nothing was recorded yet, so this read was unblocked by something
			// other than a teardown we know about. Latch the cancellation: the
			// stream is closed underneath, so leaving it unset makes a later Recv
			// report an orderly io.EOF for a local cancellation.
			t.poison(ctxErr)
			return Message{}, ctxErr
		}
		// A cancellation landing after those checks but before the deferred stop
		// must not be reported as a clean end of stream.
		if err := t.latchLateCancel(ctx, stop); err != nil {
			return Message{}, err
		}
		// A read that consumed no bytes left the framing intact but the stream
		// broken; a clean io.EOF is the stream simply ending. Latch the former
		// so every later call reports it rather than a bare close error.
		if !errors.Is(err, io.EOF) {
			t.poison(err)
		}
		return Message{}, err
	}
	// A complete line can still arrive after the context was canceled — bufio
	// may have prefetched it — and cancellation still wins: the caller asked to
	// stop and the stream is being closed under this call.
	if ctxErr := ctx.Err(); ctxErr != nil {
		t.poison(ctxErr)
		return Message{}, ctxErr
	}
	var msg Message
	if err := decodeFrame(&msg, line); err != nil {
		// Decoding a large frame takes long enough for a cancellation to land and
		// close the stream, so the same latch applies before reporting a decode
		// failure — otherwise later calls see a raw close error for a teardown
		// the caller asked for.
		if err := t.latchLateCancel(ctx, stop); err != nil {
			return Message{}, err
		}
		return Message{}, err
	}
	// Same window on the read side: a cancellation after the check above and
	// before the deferred stop closed the stream, so the message is not
	// delivered and the cancellation is latched.
	if err := t.latchLateCancel(ctx, stop); err != nil {
		return Message{}, err
	}
	// Same on the read side: a Close that landed while this frame was being read
	// or decoded must not be answered with a message from a closed transport.
	if pErr := t.poisonErr(); pErr != nil {
		return Message{}, pErr
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
				// The rest of the oversize frame is still on the wire, so the
				// stream is out of alignment from here on.
				t.poison(ErrStreamFrameTooLarge)
				return nil, ErrStreamFrameTooLarge
			}
			continue
		}
		if err != nil {
			line = append(line, chunk...)
			switch {
			case errors.Is(err, io.EOF) && len(line) == 0:
				return nil, io.EOF
			case len(line) > t.limit:
				// The frame had already outgrown the limit before the stream
				// ended, so the size is the more specific cause.
				t.poison(ErrStreamFrameTooLarge)
				return nil, ErrStreamFrameTooLarge
			case errors.Is(err, io.EOF):
				// The stream ended mid-frame. Poisoning keeps that sticky: a
				// later Recv would otherwise report a clean io.EOF, which reads
				// as an orderly close rather than a truncated message.
				t.poison(io.ErrUnexpectedEOF)
				return nil, io.ErrUnexpectedEOF
			default:
				// A read error that consumed part of a frame ends it mid-message,
				// and those bytes are gone: resuming would read this frame's tail
				// as the next message. With NO bytes consumed nothing is broken
				// by the read itself — and the zero-byte error a cancellation's
				// Close produces arrives this way — so the cause is left to Recv,
				// which knows whether the context was canceled.
				if len(line) > 0 {
					t.poison(err)
				}
				return nil, err
			}
		}
		line = append(line, chunk...)
		// The trailing newline is framing, not payload: a frame whose payload is
		// exactly the limit is in bounds. An over-limit frame is terminal on this
		// path too: whether the line happened to fit the reader's buffer must not
		// decide whether a protocol violation kills the stream.
		if len(line)-1 > t.limit {
			t.poison(ErrStreamFrameTooLarge)
			return nil, ErrStreamFrameTooLarge
		}
		return line[:len(line)-1], nil
	}
}

// poison records the failure that broke the framing and closes the stream, once.
// Every later call returns that failure. The close happens outside the lock:
// closing can block, and every call that reads the recorded cause would wait
// behind it.
//
// It deliberately does NOT take opMu. Taking the write lock here would wait for
// a write already in flight — and this close is what unblocks that write — so
// poisoning would deadlock against the very write it exists to interrupt. The
// consequence is that a write already past its latch check can still reach the
// stream; it is writing to a stream this call just closed, its own error path
// reports the recorded cause, and nothing new can be admitted.
func (t *StreamTransport) poison(err error) {
	t.mu.Lock()
	first := t.poisoned == nil
	if first {
		t.poisoned = err
	}
	t.mu.Unlock()
	if first {
		_ = t.doClose()
	}
}

// doClose closes the underlying stream, once. A caller arriving while the close
// is in progress waits for it: io.Closer's post-close behavior is undefined, and
// a second caller must not be able to report the transport closed while the
// first close is still running.
func (t *StreamTransport) doClose() error {
	t.closeOnce.Do(func() {
		t.closeErr = t.rw.Close()
	})
	return t.closeErr
}

func (t *StreamTransport) poisonErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.poisoned
}

// latchLateCancel closes the window between a caller's last context check and
// its deferred stop. Every path that can return has to apply the same rule, and
// four copies of it drifted apart once already.
//
// The context is the authority, not stop(): stop reports whether it suppressed
// the callback, and a cancellation can land in the same instant and be
// suppressed with it, in which case nobody has latched anything. So the context
// is checked unconditionally, and a live context is the only case that returns
// nil.
func (t *StreamTransport) latchLateCancel(ctx context.Context, stop func() bool) error {
	stop()
	ctxErr := ctx.Err()
	if ctxErr == nil {
		return nil
	}
	t.poison(ctxErr)
	return ctxErr
}

// Close ends the transport. The closed state is latched, so Send and Recv keep
// reporting ErrStreamClosed afterwards even when the reader still holds frames
// it prefetched. Only the call that latches reaches the underlying closer:
// io.Closer's post-close behavior is undefined, so a repeat must not call an
// arbitrary closer a second time.
func (t *StreamTransport) Close() error {
	t.mu.Lock()
	first := t.poisoned == nil
	if first {
		t.poisoned = ErrStreamClosed
	}
	t.mu.Unlock()
	if !first {
		// Already terminal, but the close that poisoned it may still be running
		// and a write admitted before it may still be in flight. Wait for the
		// close (doClose returns as soon as the one real close has finished) and
		// then drain, so returning here never reports the transport closed while
		// the stream is still open or a writer can still reach it.
		_ = t.doClose()
		t.drainWrites()
		return nil
	}
	err := t.doClose()
	// Wait for admitted writes to finish: after this returns, no write can still
	// reach the stream. The close above is what unblocks one already in flight,
	// so this cannot wait forever on a write that is merely stalled.
	t.drainWrites()
	return err
}

// drainWrites blocks until every admitted write has finished. It is an empty
// critical section on purpose — acquiring the write lock IS the wait — so both
// of gocritic's lock heuristics misread it: badLock sees the adjacent
// Lock/Unlock as a mistake, and unnecessaryDefer sees a defer with nothing after
// it. Neither applies to a barrier.
func (t *StreamTransport) drainWrites() {
	t.opMu.Lock()
	//nolint:gocritic // deliberate barrier, not a forgotten defer or a stray unlock
	defer t.opMu.Unlock()
}
