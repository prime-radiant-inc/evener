package appwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestStreamTransportRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup
	ta := NewStreamTransport(a)
	tb := NewStreamTransport(b)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		msg, err := tb.Recv(ctx)
		if err != nil {
			serverErr <- fmt.Errorf("server recv: %w", err)
			return
		}
		if msg.Request == nil || msg.Request.Method != MethodThreadList {
			serverErr <- fmt.Errorf("server got %+v, want thread/list request", msg)
			return
		}
		serverErr <- tb.Send(ctx, ResponseMessage(msg.Request.ID, json.RawMessage(`{"ok":true}`)))
	}()

	if err := ta.Send(ctx, RequestMessage(NewIntID(1), MethodThreadList, ThreadListParams{})); err != nil {
		t.Fatalf("client send: %v", err)
	}
	resp, err := ta.Recv(ctx)
	if err != nil {
		t.Fatalf("client recv: %v", err)
	}
	if resp.Response == nil {
		t.Fatalf("client got %+v, want response", resp)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

// TestStreamTransportBacksClient proves the stream transport is a drop-in for
// the appwire.Client, which is what a controller would use over SSH.
func TestStreamTransportBacksClient(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server := NewStreamTransport(b)
	go func() {
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			var result []byte
			switch msg.Request.Method {
			case MethodInitialize:
				result, _ = json.Marshal(InitializeResponse{ProtocolVersion: ProtocolVersion, SourceID: "local"})
			case MethodThreadList:
				result, _ = json.Marshal(ThreadListResponse{})
			}
			_ = server.Send(ctx, ResponseMessage(msg.Request.ID, json.RawMessage(result)))
		}
	}()

	c := NewClient(NewStreamTransport(a))
	c.Start(ctx)
	defer c.Close() //nolint:errcheck // test cleanup

	init, err := c.Initialize(ctx, InitializeParams{ClientInfo: ClientInfo{Name: "spike"}})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if init.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol = %q, want %q", init.ProtocolVersion, ProtocolVersion)
	}
	if _, err := c.ThreadList(ctx, ThreadListParams{}); err != nil {
		t.Fatalf("thread/list: %v", err)
	}
}

// feedLine writes raw bytes to the far end of a fresh pipe and returns the read
// side's transport. The write happens in a goroutine because net.Pipe is
// synchronous.
func feedLine(t *testing.T, limit int, raw []byte, closeAfter bool) *StreamTransport {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	go func() {
		_, _ = a.Write(raw)
		if closeAfter {
			_ = a.Close()
		}
	}()
	return NewStreamTransportWithLimit(b, limit)
}

// The off-by-one this covers: a payload of exactly the limit plus its trailing
// newline needs limit+1 bytes on the wire, so a reader capped at exactly limit
// rejected a frame the writer had accepted, and the stream never recovered.
func TestStreamTransportAcceptsFrameExactlyAtLimit(t *testing.T) {
	tr := feedLine(t, 4, []byte("abcd\n"), false)

	line, err := tr.readLine()
	if err != nil {
		t.Fatalf("readLine = %v, want the frame at the limit to be accepted", err)
	}
	if string(line) != "abcd" {
		t.Fatalf("readLine = %q, want %q", line, "abcd")
	}
}

func TestStreamTransportRejectsFrameOverLimit(t *testing.T) {
	tr := feedLine(t, 4, []byte("abcde\n"), true)

	if _, err := tr.readLine(); !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("readLine err = %v, want ErrStreamFrameTooLarge", err)
	}
}

// A final line with no trailing newline is a torn write, not a short message:
// decoding it would hand a truncated frame to the caller as if it were whole.
func TestStreamTransportRejectsTornFrame(t *testing.T) {
	tr := feedLine(t, 64, []byte("abcd"), true)

	if _, err := tr.readLine(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("readLine err = %v, want io.ErrUnexpectedEOF", err)
	}
	// A torn frame is terminal, not a clean end of stream: a later Recv must
	// report the truncation rather than an orderly io.EOF.
	if _, err := tr.Recv(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Recv after a torn frame = %v, want the poisoning io.ErrUnexpectedEOF", err)
	}
}

func TestStreamTransportCleanEOFIsEOF(t *testing.T) {
	tr := feedLine(t, 64, nil, true)

	if _, err := tr.readLine(); !errors.Is(err, io.EOF) {
		t.Fatalf("readLine err = %v, want io.EOF", err)
	}
}

// Newline framing is safe only because marshaling cannot emit a raw newline:
// json.Marshal compacts a json.RawMessage's inter-token whitespace away, and a
// raw newline inside a string is invalid JSON it refuses outright. Pinning that
// here is what lets Send write a frame without scanning it for '\n'.
func TestStreamTransportMarshalNeverEmitsNewline(t *testing.T) {
	for _, result := range []json.RawMessage{
		json.RawMessage("{\"a\":\n1}"),
		json.RawMessage("{\n  \"a\": 1,\n  \"b\": 2\n}"),
		json.RawMessage("{\"a\":\r\n1}"),
		json.RawMessage(`{"a":"plain"}`),
	} {
		data, err := marshalWSMessage(ResponseMessage(NewIntID(1), result))
		if err != nil {
			t.Fatalf("marshal(%q): %v", result, err)
		}
		if bytes.ContainsAny(data, "\n\r") {
			t.Fatalf("marshal(%q) = %q, want no raw newline", result, data)
		}
	}

	// A raw newline inside a string literal reaches the wire only if marshaling
	// lets it through, so it must be an error rather than a split frame.
	if _, err := marshalWSMessage(ResponseMessage(NewIntID(1), json.RawMessage("{\"a\":\"x\ny\"}"))); err == nil {
		t.Fatal("marshal accepted a raw newline inside a string literal")
	}
}

func TestStreamTransportSendRejectsOversizeFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup

	tr := NewStreamTransportWithLimit(a, 8)
	msg := ResponseMessage(NewIntID(1), json.RawMessage(`{"payload":"far longer than eight bytes"}`))
	if err := tr.Send(context.Background(), msg); !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("Send err = %v, want ErrStreamFrameTooLarge", err)
	}
}

// TestStreamTransportRejectsOversizeFrame proves the read limit is enforced
// while reading: a newline-free frame past the cap fails without buffering the
// whole frame.
func TestStreamTransportRejectsOversizeFrame(t *testing.T) {
	const limit = 64
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup
	tr := NewStreamTransportWithLimit(b, limit)

	go func() {
		// No newline, so no frame can complete within the cap.
		_, _ = a.Write(bytes.Repeat([]byte("x"), 4*limit))
		_ = a.Close()
	}()

	_, err := tr.Recv(context.Background())
	if !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("Recv err = %v, want ErrStreamFrameTooLarge", err)
	}
}

// The stream has no deadline API, so a canceled ctx closes it to unblock a Recv
// that is waiting on a peer that will never write.
func TestStreamTransportRecvUnblocksOnCancel(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup

	tr := NewStreamTransport(b)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := tr.Recv(ctx)
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Recv err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Recv did not return after cancellation")
	}
}

// Same contract on the write path: a peer that has stopped reading must not pin
// Send (and the send mutex behind it) forever.
func TestStreamTransportSendUnblocksOnCancel(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close() //nolint:errcheck // test cleanup

	tr := NewStreamTransport(a)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- tr.Send(ctx, ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`)))
	}()

	// net.Pipe writes block until the far end reads, which it never does.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after cancellation")
	}
}

// blockingWriteStream stalls inside Write until released, so a test can hold the
// transport's write lock deterministically instead of racing a sleep.
type blockingWriteStream struct {
	release chan struct{}
	mu      sync.Mutex
	writes  int
}

func (s *blockingWriteStream) Read([]byte) (int, error) { return 0, io.EOF }

func (s *blockingWriteStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	<-s.release
	return len(p), nil
}

func (s *blockingWriteStream) Close() error { return nil }

func (s *blockingWriteStream) waitForWrites(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := s.writes
		s.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("write never started")
}

// shortWriteStream accepts only part of every write, the way a stream that fails
// mid-frame behaves.
type shortWriteStream struct {
	io.Reader
	limit int
}

func (s *shortWriteStream) Write([]byte) (int, error) { return s.limit, nil }
func (s *shortWriteStream) Close() error              { return nil }

// memoryStream serves reads from a fixed byte stream and records writes, so a
// test can feed frames and assert what was written without net.Pipe's
// synchronous handshake.
type memoryStream struct {
	r *bytes.Reader
	w bytes.Buffer
}

func (m *memoryStream) Read(p []byte) (int, error)  { return m.r.Read(p) }
func (m *memoryStream) Write(p []byte) (int, error) { return m.w.Write(p) }
func (m *memoryStream) Close() error                { return nil }

// The rejections that leave the stream aligned must not kill it: a Send refused
// before it wrote, and a whole frame consumed but undecodable, both leave the
// next frame readable.
func TestStreamTransportNonTerminalRejectionsLeaveStreamUsable(t *testing.T) {
	const limit = 64
	valid, err := marshalWSMessage(ResponseMessage(NewIntID(2), json.RawMessage(`{"ok":true}`)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	feed := append([]byte("not json at all\n"), append(valid, '\n')...)
	stream := &memoryStream{r: bytes.NewReader(feed)}
	tr := NewStreamTransportWithLimit(stream, limit)

	// Refused before the write, so nothing reached the wire.
	tooBig := ResponseMessage(NewIntID(1), json.RawMessage(`{"padding":"`+string(bytes.Repeat([]byte("x"), limit))+`"}`))
	if err := tr.Send(context.Background(), tooBig); !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("Send err = %v, want ErrStreamFrameTooLarge", err)
	}
	if stream.w.Len() != 0 {
		t.Fatalf("a refused Send wrote %d bytes", stream.w.Len())
	}

	// The whole bad frame was consumed, so the valid one that follows still reads.
	if _, err := tr.Recv(context.Background()); err == nil {
		t.Fatal("Recv accepted a non-JSON frame")
	}
	if _, err := tr.Recv(context.Background()); err != nil {
		t.Fatalf("Recv after two non-terminal rejections = %v, want the valid frame", err)
	}
}

// An over-limit frame is terminal however it was detected. That matters for the
// path where the reader's buffer (bufio floors it at 16 bytes, above limit+1 for
// small limits) held the whole line: terminality must not depend on chunking.
func TestStreamTransportBufferedOversizeIsTerminal(t *testing.T) {
	const limit = 8
	// A payload over the limit whose line still fits bufio's minimum buffer.
	feed := append(bytes.Repeat([]byte("x"), limit+4), '\n')
	tr := NewStreamTransportWithLimit(&memoryStream{r: bytes.NewReader(feed)}, limit)

	if _, err := tr.Recv(context.Background()); !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("Recv err = %v, want ErrStreamFrameTooLarge", err)
	}
	if _, err := tr.Recv(context.Background()); !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("second Recv = %v, want the poisoning ErrStreamFrameTooLarge", err)
	}
}

// cancelOnWriteStream cancels the caller's context from inside Write, the way a
// cancellation racing a failing write arrives.
type cancelOnWriteStream struct {
	cancel context.CancelFunc
}

func (s *cancelOnWriteStream) Read([]byte) (int, error) { return 0, io.EOF }

func (s *cancelOnWriteStream) Write(p []byte) (int, error) {
	s.cancel()
	// One byte short and an error: part of the frame reached the wire.
	return len(p) - 1, errors.New("write failed midway")
}

func (s *cancelOnWriteStream) Close() error { return nil }

// zeroWriteErrorStream fails without putting a byte on the wire, the way a
// broken pipe, a reset, or an already-closed stream does.
type zeroWriteErrorStream struct{ err error }

func (s *zeroWriteErrorStream) Read([]byte) (int, error)  { return 0, io.EOF }
func (s *zeroWriteErrorStream) Write([]byte) (int, error) { return 0, s.err }
func (s *zeroWriteErrorStream) Close() error              { return nil }

// fullWriteErrorStream reports an error after writing the whole frame, which
// io.Writer permits.
type fullWriteErrorStream struct{ err error }

func (s *fullWriteErrorStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *fullWriteErrorStream) Write(p []byte) (int, error) { return len(p), s.err }
func (s *fullWriteErrorStream) Close() error                { return nil }

// A zero-byte write failure must surface the real cause: calling it a short
// write would discard EPIPE/net.ErrClosed and claim part of a frame was sent.
func TestStreamTransportZeroByteWriteErrorIsNotShortWrite(t *testing.T) {
	writeErr := errors.New("broken pipe")
	tr := NewStreamTransport(&zeroWriteErrorStream{err: writeErr})

	err := tr.Send(context.Background(), ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`)))
	if !errors.Is(err, writeErr) {
		t.Fatalf("Send err = %v, want the underlying write error", err)
	}
	if errors.Is(err, io.ErrShortWrite) {
		t.Fatal("a zero-byte write failure was reported as a short write")
	}
}

// A write that reports an error after landing the whole frame is still sticky:
// the caller cannot tell whether the peer flushed it, so the transport is done.
func TestStreamTransportFullWriteWithErrorPoisons(t *testing.T) {
	writeErr := errors.New("write failed")
	tr := NewStreamTransport(&fullWriteErrorStream{err: writeErr})

	msg := ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`))
	if err := tr.Send(context.Background(), msg); !errors.Is(err, writeErr) {
		t.Fatalf("Send err = %v, want the write error", err)
	}
	if err := tr.Send(context.Background(), msg); !errors.Is(err, writeErr) {
		t.Fatalf("second Send err = %v, want the sticky write error", err)
	}
}

// A canceled Recv closes the stream underneath itself, so the cancellation has
// to latch: otherwise a later Recv reports a clean io.EOF for a local stop.
func TestStreamTransportCanceledRecvLatches(t *testing.T) {
	stream := &parkedReadStream{entered: make(chan struct{}, 1), release: make(chan struct{})}
	tr := NewStreamTransport(stream)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		_, err := tr.Recv(ctx)
		done <- err
	}()
	// Wait until Recv is provably parked inside Read, so the cancellation below
	// cannot land before the call that has to latch it.
	select {
	case <-stream.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Recv never reached the stream")
	}
	cancel()
	close(stream.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Recv err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Recv did not return after cancellation")
	}

	// The stream ends here; a non-latching transport would report a clean EOF.
	if _, err := tr.Recv(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv after a canceled Recv = %v, want the latched context.Canceled", err)
	}
}

// parkedReadStream signals when a Read has started and then blocks until
// released, so a test can cancel provably in the middle of a read.
type parkedReadStream struct {
	entered chan struct{}
	release chan struct{}
}

// signalingPipeEnd wraps a real pipe end and signals when a Read has started.
// Unlike parkedReadStream its Close is the real one, so a cancel unblocks the
// read with an error — the path the SSH channel actually takes.
type signalingPipeEnd struct {
	net.Conn
	entered chan struct{}
}

func (s *signalingPipeEnd) Read(p []byte) (int, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	return s.Conn.Read(p)
}

// The parked-read test above uses a stream whose Close is a no-op, so it never
// exercises the cancel-induced close error path. Here the close really
// interrupts the read, and the cancellation — not the close error it produced —
// must be what later calls report.
func TestStreamTransportRealCloseLatchReportsCancellation(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	reader := &signalingPipeEnd{Conn: b, entered: make(chan struct{}, 1)}
	tr := NewStreamTransport(reader)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		_, err := tr.Recv(ctx)
		done <- err
	}()
	select {
	case <-reader.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Recv never reached the stream")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Recv err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Recv did not return after cancellation")
	}

	if _, err := tr.Recv(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv after the cancel-induced close = %v, want the latched context.Canceled", err)
	}
}

// The production limit is far larger than the reader's 4096-byte buffer, so a
// frame arrives as several chunks and the size check has to decide after the
// line has outgrown one buffer. Every other test uses a limit below the buffer
// size, where the first full buffer already proves oversize.
func TestStreamTransportMultiChunkFrame(t *testing.T) {
	const limit = 8192
	payload := string(bytes.Repeat([]byte("x"), limit-256))
	frame, err := marshalWSMessage(ResponseMessage(NewIntID(1), json.RawMessage(`{"pad":"`+payload+`"}`)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(frame) <= 4096 || len(frame) > limit {
		t.Fatalf("frame is %d bytes; the test needs it above the 4096-byte buffer and within the %d limit", len(frame), limit)
	}
	tr := NewStreamTransportWithLimit(&memoryStream{r: bytes.NewReader(append(frame, '\n'))}, limit)
	if _, err := tr.Recv(context.Background()); err != nil {
		t.Fatalf("Recv of a multi-chunk frame within the limit = %v, want success", err)
	}

	// And one past the limit fails only after growing across chunks.
	over := append(bytes.Repeat([]byte("x"), limit+512), '\n')
	trOver := NewStreamTransportWithLimit(&memoryStream{r: bytes.NewReader(over)}, limit)
	if _, err := trOver.Recv(context.Background()); !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("Recv err = %v, want ErrStreamFrameTooLarge", err)
	}
}

func (s *parkedReadStream) Read([]byte) (int, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return 0, io.EOF
}

func (s *parkedReadStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *parkedReadStream) Close() error                { return nil }

// Close latches: a frame bufio already prefetched must not be delivered by a
// Recv after the caller closed the transport.
func TestStreamTransportCloseLatchesClosedState(t *testing.T) {
	var frames []byte
	for id := range 2 {
		frame, err := marshalWSMessage(ResponseMessage(NewIntID(int64(id+1)), json.RawMessage(`{"ok":true}`)))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		frames = append(append(frames, frame...), '\n')
	}
	tr := NewStreamTransport(&memoryStream{r: bytes.NewReader(frames)})

	if _, err := tr.Recv(context.Background()); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := tr.Recv(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Recv after Close = %v, want ErrStreamClosed", err)
	}
	if err := tr.Send(context.Background(), ResponseMessage(NewIntID(3), json.RawMessage(`{}`))); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Send after Close = %v, want ErrStreamClosed", err)
	}
}

// A write that fails as the context is canceled has still desynchronized the
// stream, so the next Send must find a poisoned transport rather than appending
// a whole frame after the orphaned bytes. WHICH cause is recorded is a race this
// test cannot control: the cancel callback latches the cancellation and the
// write path latches the short write, and both are terminal for the stream. The
// property under test is that it is poisoned at all.
func TestStreamTransportCanceledPartialWriteStillPoisons(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	tr := NewStreamTransport(&cancelOnWriteStream{cancel: cancel})

	_ = tr.Send(ctx, ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`)))

	err := tr.Send(context.Background(), ResponseMessage(NewIntID(2), json.RawMessage(`{"ok":true}`)))
	if err == nil {
		t.Fatal("a transport with a canceled partial write still accepted a frame")
	}
	if !errors.Is(err, io.ErrShortWrite) && !errors.Is(err, context.Canceled) {
		t.Fatalf("Send after a canceled partial write = %v, want the short write or the cancellation", err)
	}
}

// A non-positive limit is clamped rather than leaving a transport whose reader
// buffer is sized at zero and which rejects every frame.
func TestStreamTransportLimitIsClamped(t *testing.T) {
	tr := NewStreamTransportWithLimit(&memoryStream{r: bytes.NewReader([]byte("a\n"))}, 0)

	line, err := tr.readLine()
	if err != nil {
		t.Fatalf("readLine with a non-positive limit = %v, want the frame accepted", err)
	}
	if string(line) != "a" {
		t.Fatalf("readLine = %q, want %q", line, "a")
	}
}

// A send queued behind another stalled write must obey its own context rather
// than wait for a lock it may never get.
func TestStreamTransportQueuedSendHonorsCancel(t *testing.T) {
	st := &blockingWriteStream{release: make(chan struct{})}
	tr := NewStreamTransport(st)

	holder := make(chan error, 1)
	go func() {
		holder <- tr.Send(context.Background(), ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`)))
	}()
	// Once the first send is inside Write it owns the lock.
	st.waitForWrites(t, 1)

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { queued <- tr.Send(queuedCtx, ResponseMessage(NewIntID(2), json.RawMessage(`{"ok":true}`))) }()
	cancelQueued()

	select {
	case err := <-queued:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Send err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a Send queued on the write lock did not honor its canceled context")
	}

	close(st.release)
	<-holder
}

// A send that was already queued on the write lock when the transport got
// poisoned must observe the poisoning, not write into a stream that is gone.
func TestStreamTransportQueuedSendSeesPoison(t *testing.T) {
	st := &blockingWriteStream{release: make(chan struct{})}
	tr := NewStreamTransport(st)

	holder := make(chan error, 1)
	go func() {
		holder <- tr.Send(context.Background(), ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`)))
	}()
	st.waitForWrites(t, 1)

	queued := make(chan error, 1)
	go func() {
		queued <- tr.Send(context.Background(), ResponseMessage(NewIntID(2), json.RawMessage(`{"ok":true}`)))
	}()

	// Poison while the first send still holds the lock, then let it finish.
	tr.poison(io.ErrShortWrite)
	close(st.release)
	<-holder

	select {
	case err := <-queued:
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("queued Send err = %v, want the poisoning io.ErrShortWrite", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued Send did not return")
	}
}

// A canceled send that never wrote a byte must not tear the stream down for the
// callers that are still using it.
func TestStreamTransportCanceledSendLeavesStreamUsable(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup
	tr := NewStreamTransport(b)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tr.Send(ctx, ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`))); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send err = %v, want context.Canceled", err)
	}

	frame, err := marshalWSMessage(ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`)))
	if err != nil {
		t.Fatalf("marshal probe frame: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := tr.Recv(context.Background())
		done <- err
	}()
	if _, err := a.Write(append(frame, '\n')); err != nil {
		t.Fatalf("write after the canceled Send: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Recv after a canceled Send: %v, want the stream to still work", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was torn down by a Send that never wrote")
	}
}

// An oversize frame leaves its tail on the wire, so the transport poisons
// itself: a reader that resynchronized would decode the tail as a message.
func TestStreamTransportOversizeFramePoisons(t *testing.T) {
	const limit = 64
	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	tr := NewStreamTransportWithLimit(b, limit)

	go func() {
		// An oversize frame with no newline, immediately followed by bytes that
		// look like a valid frame.
		_, _ = a.Write(bytes.Repeat([]byte("x"), 4*limit))
		_, _ = a.Write([]byte("{\"response\":{\"id\":1,\"result\":{}}}\n"))
	}()

	if _, err := tr.Recv(context.Background()); !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("Recv err = %v, want ErrStreamFrameTooLarge", err)
	}
	if _, err := tr.Recv(context.Background()); !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("second Recv err = %v, want the poisoning ErrStreamFrameTooLarge", err)
	}
}

// A short write leaves a partial frame on the wire, which cannot be repaired, so
// every later call must fail rather than write into a desynchronized stream.
func TestStreamTransportPartialWritePoisons(t *testing.T) {
	tr := NewStreamTransport(&shortWriteStream{Reader: bytes.NewReader(nil), limit: 4})

	err := tr.Send(context.Background(), ResponseMessage(NewIntID(1), json.RawMessage(`{"ok":true}`)))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Send err = %v, want io.ErrShortWrite", err)
	}
	if err := tr.Send(context.Background(), ResponseMessage(NewIntID(2), json.RawMessage(`{"ok":true}`))); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("second Send err = %v, want the poisoning io.ErrShortWrite", err)
	}
}
