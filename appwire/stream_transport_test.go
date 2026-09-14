package appwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// TestStreamTransportRejectsOversizeFrame proves the read limit is enforced
// during the read: a newline-free frame past the cap fails without buffering
// the whole frame.
func TestStreamTransportRejectsOversizeFrame(t *testing.T) {
	old := streamFrameLimit
	streamFrameLimit = 64
	t.Cleanup(func() { streamFrameLimit = old })

	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup
	tr := NewStreamTransport(b)

	go func() {
		// No newline, so the scanner cannot complete a token within the cap.
		_, _ = a.Write(bytes.Repeat([]byte("x"), 4*streamFrameLimit))
		_ = a.Close()
	}()

	_, err := tr.Recv(context.Background())
	if !errors.Is(err, errStreamFrameTooLarge) {
		t.Fatalf("Recv err = %v, want errStreamFrameTooLarge", err)
	}
}
