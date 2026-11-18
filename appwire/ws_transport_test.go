package appwire

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

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

type wsTransportPingContextKey struct{}

type wsTransportPingError struct{}

func (*wsTransportPingError) Error() string { return "ping failed" }

// TestWSTransportPing pins the WebSocket boundary: Ping must pass its own
// connection and the caller's context through unchanged, and return the
// boundary error unchanged.
func TestWSTransportPing(t *testing.T) {
	oldPing := pingWebSocket
	t.Cleanup(func() { pingWebSocket = oldPing })

	conn := &websocket.Conn{}
	successCtx := context.WithValue(t.Context(), wsTransportPingContextKey{}, "success")
	errorCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	wantErr := &wsTransportPingError{}
	wantContexts := []context.Context{successCtx, errorCtx}
	calls := 0
	pingWebSocket = func(gotConn *websocket.Conn, gotCtx context.Context) error {
		if gotConn != conn {
			t.Fatalf("ping receiver = %p, want %p", gotConn, conn)
		}
		if calls >= len(wantContexts) {
			t.Fatalf("unexpected ping call %d", calls+1)
		}
		if gotCtx != wantContexts[calls] {
			t.Fatalf("ping context on call %d was replaced", calls+1)
		}
		calls++
		if gotCtx == errorCtx {
			return wantErr
		}
		return nil
	}

	tr := &WSTransport{conn: conn}
	if err := tr.Ping(successCtx); err != nil {
		t.Fatalf("Ping success: unexpected error: %v", err)
	}
	err := tr.Ping(errorCtx)
	if reflect.TypeOf(err) != reflect.TypeOf(wantErr) || reflect.ValueOf(err).Pointer() != reflect.ValueOf(wantErr).Pointer() {
		t.Fatalf("Ping error = %T %v, want exact input error %p", err, err, wantErr)
	}
	if calls != 2 {
		t.Fatalf("ping calls = %d, want 2", calls)
	}
}

// TestWSTransportSendDetachesCallerCancellation pins issue #6824 at the library
// boundary. coder/websocket registers a context.AfterFunc on each write's
// context and closes the whole connection when that context ends (conn.go's
// setupWriteTimeout). A transport carries many callers' frames over one
// connection, so a write that kept one caller's cancellation let that caller's
// cancel close the shared socket: the hub's cancel of a relay read closed the
// daemon connection, and the read that followed failed with "use of closed
// network connection" although nobody had closed it.
//
// The seam is the exact context handed to the library write, so the assertion
// is about what the library is told to watch, not about this package's own
// bookkeeping. The check happens while the write is still in flight, which is
// the moment the library's AfterFunc would close the connection. The caller's
// values and deadline must survive: a deadline still bounds a wedged peer, and
// only the cancellation is dropped.
func TestWSTransportSendDetachesCallerCancellation(t *testing.T) {
	realWrite := writeWebSocket
	t.Cleanup(func() { writeWebSocket = realWrite })
	entered, release := make(chan context.Context, 1), make(chan struct{})
	writeWebSocket = func(_ *websocket.Conn, ctx context.Context, _ websocket.MessageType, _ []byte) error {
		entered <- ctx
		<-release
		return nil
	}
	transport := &WSTransport{conn: &websocket.Conn{}}

	deadline := time.Now().Add(time.Minute)
	callerCtx, cancelCaller := context.WithDeadline(context.WithValue(t.Context(), wsTransportPingContextKey{}, "value"), deadline)
	done := make(chan error, 1)
	go func() {
		done <- transport.Send(callerCtx, RequestMessage(NewIntID(1), MethodThreadList, ThreadListParams{}))
	}()
	var writeCtx context.Context
	select {
	case writeCtx = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the write never reached the transport")
	}
	if writeCtx == callerCtx {
		t.Fatal("the write ran on the caller's own context: its cancellation would close the shared connection")
	}
	cancelCaller()
	if err := writeCtx.Err(); err != nil {
		t.Fatalf("the in-flight write context ended with the caller's cancellation: %v", err)
	}
	if got := writeCtx.Value(wsTransportPingContextKey{}); got != "value" {
		t.Fatalf("write context value = %v, want the caller's value carried through", got)
	}
	gotDeadline, ok := writeCtx.Deadline()
	if !ok || !gotDeadline.Equal(deadline) {
		t.Fatalf("write context deadline = %v (%t), want the caller's deadline %v kept", gotDeadline, ok, deadline)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("send: %v", err)
	}
}
