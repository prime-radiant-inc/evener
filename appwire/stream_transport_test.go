package appwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
