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
