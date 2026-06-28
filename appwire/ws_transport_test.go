package appwire

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestWSTransportRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		_, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if msg.Request == nil || msg.Request.Method != MethodThreadList {
			t.Errorf("message=%+v", msg)
			return
		}
		out, err := json.Marshal(ResponseMessage(msg.Request.ID, ThreadListResponse{}))
		if err != nil {
			t.Errorf("marshal: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, out); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := DialWebSocket(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()

	if err := transport.Send(ctx, RequestMessage(NewIntID(1), MethodThreadList, ThreadListParams{})); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := transport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if resp.Response == nil || resp.Response.ID.Int64() != 1 {
		t.Fatalf("resp=%+v", resp)
	}
}

// TestWSTransportReceivesLargeAppWireMessage sends a response larger than the
// WebSocket library's built-in default read limit (32 KiB) and asserts that
// the transport delivers it correctly. This implicitly validates that
// NewWSTransport called SetReadLimit on the underlying connection; without that
// call, the read would fail with ErrMessageTooBig.
//
// Combined with TestWSTransportReadLimitCoversMaxComposerImages (which validates
// the constant's arithmetic), the two tests together verify that the limit is
// (a) set at all and (b) large enough for the expected payload sizes.
//
// Remaining gap: if NewWSTransport hardcodes a limit of the same order of
// magnitude as appWireWebSocketReadLimit (e.g. 64 MiB instead of 128 MiB), the
// discrepancy is not caught here. Closing that gap would require either a
// payload >64 MiB (impractical in a unit test) or an exported getter on
// WSTransport, neither of which is worth the cost at this risk level.
func TestWSTransportReceivesLargeAppWireMessage(t *testing.T) {
	// websocket default read limit; payload must exceed this to exercise SetReadLimit.
	const wsDefaultReadLimit = 32768

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		_, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Errorf("decode: %v", err)
			return
		}

		largePreview := strings.Repeat("large session preview ", 4096)
		if len(largePreview) <= wsDefaultReadLimit {
			// Guard: keep the payload above the library default so the test
			// stays meaningful if Repeat's argument is ever reduced.
			t.Errorf("test payload (%d bytes) must exceed WebSocket default read limit (%d bytes)",
				len(largePreview), wsDefaultReadLimit)
			return
		}
		out, err := json.Marshal(ResponseMessage(msg.Request.ID, ThreadListResponse{Data: []Thread{{
			ID:      "th_large",
			Preview: largePreview,
		}}}))
		if err != nil {
			t.Errorf("marshal: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, out); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := DialWebSocket(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()

	if err := transport.Send(ctx, RequestMessage(NewIntID(1), MethodThreadList, ThreadListParams{})); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := transport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if resp.Response == nil || resp.Response.ID.Int64() != 1 {
		t.Fatalf("resp=%+v", resp)
	}
}

// TestWSTransportReadLimitCoversMaxComposerImages validates that the
// appWireWebSocketReadLimit constant is arithmetically large enough to receive a
// message containing the maximum number of base64-encoded composer images.
//
// This test checks the constant's value only. TestWSTransportReceivesLargeAppWireMessage
// validates that NewWSTransport actually applies the limit to the connection.
// Together they cover both the magnitude of the limit and its application.
func TestWSTransportReadLimitCoversMaxComposerImages(t *testing.T) {
	const (
		maxImages     = 8
		maxImageBytes = 8 * 1024 * 1024
		jsonHeadroom  = 1024 * 1024
	)
	encodedImageBytes := ((maxImageBytes + 2) / 3) * 4
	encodedPayloadBytes := maxImages*encodedImageBytes + jsonHeadroom
	if appWireWebSocketReadLimit < encodedPayloadBytes {
		t.Fatalf("appWireWebSocketReadLimit=%d, want at least %d", appWireWebSocketReadLimit, encodedPayloadBytes)
	}
}
