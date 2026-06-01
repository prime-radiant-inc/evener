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

func TestWSTransportReceivesLargeAppWireMessage(t *testing.T) {
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
