package appwire

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

type capturedFrameObserver struct {
	sent     [][]byte
	received [][]byte
}

func (o *capturedFrameObserver) RecordSend(data []byte) {
	o.sent = append(o.sent, append([]byte(nil), data...))
}

func (o *capturedFrameObserver) RecordRecv(data []byte) {
	o.received = append(o.received, append([]byte(nil), data...))
}

func TestObservedWSTransportReportsExactWireBytes(t *testing.T) {
	const response = `{ "id": 7, "result": {} }`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // test cleanup
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(response)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	observer := &capturedFrameObserver{}
	transport := NewObservedWSTransport(conn, observer)
	defer transport.Close() //nolint:errcheck // test cleanup

	if err := transport.Send(ctx, RequestMessage(NewIntID(7), MethodPing, EmptyParams{})); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := transport.Recv(ctx); err != nil {
		t.Fatalf("recv: %v", err)
	}

	if len(observer.sent) != 1 || string(observer.sent[0]) != `{"id":7,"method":"ping","params":{}}` {
		t.Fatalf("observed sends = %q, want exact marshaled request", observer.sent)
	}
	if len(observer.received) != 1 || string(observer.received[0]) != response {
		t.Fatalf("observed receives = %q, want exact response %q", observer.received, response)
	}
}

func TestObservedWSTransportDoesNotReportFailedSend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()

	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	observer := &capturedFrameObserver{}
	transport := NewObservedWSTransport(conn, observer)
	conn.CloseNow()

	if err := transport.Send(ctx, RequestMessage(NewIntID(8), MethodPing, EmptyParams{})); err == nil {
		t.Fatal("Send succeeded after the WebSocket was closed")
	}
	if len(observer.sent) != 0 {
		t.Fatalf("observed sends = %q, want none for failed write", observer.sent)
	}
}
