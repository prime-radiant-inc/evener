package appwire

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

var (
	marshalWSMessage = json.Marshal
	// Frames decode through Message.UnmarshalJSON directly: json.Unmarshal would
	// first scan the whole frame only to hand the same bytes to that method,
	// which validates them itself. Both transports decode through it.
	decodeFrame    = (*Message).UnmarshalJSON
	pingWebSocket  = (*websocket.Conn).Ping
	readWebSocket  = (*websocket.Conn).Read
	writeWebSocket = (*websocket.Conn).Write
)

// frameWriteContext derives the context one frame write runs under. It keeps
// the caller's values and deadline and drops the caller's cancellation:
// coder/websocket installs a context.AfterFunc on the write context that closes
// the whole connection when it ends (conn.go's setupWriteTimeout), so honoring
// one caller's cancellation there tears down a socket every caller on this
// transport shares. The next frame then fails with "use of closed network
// connection" — issue #6824, where a canceled relay read closed the hub's
// daemon connection under the read that followed it. A deadline still applies,
// so the server write timeout and any caller deadline keep bounding a wedged
// peer, and a dropped peer still unblocks the write through the keepalive
// closing the transport.
func frameWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(ctx)
	deadline, ok := ctx.Deadline()
	if !ok {
		return detached, func() {}
	}
	return context.WithDeadline(detached, deadline)
}

// FrameObserver receives the exact JSON bytes sent and received by one
// WebSocket transport. Implementations must consume or copy data before the
// method returns.
type FrameObserver interface {
	RecordSend([]byte)
	RecordRecv([]byte)
}

type WSTransport struct {
	conn     *websocket.Conn
	rec      *FrameRecorder // nil unless EVENER_RECORD_APPWIRE selected recording
	observer FrameObserver
}

const appWireWebSocketReadLimit = 128 << 20

func DialWebSocket(ctx context.Context, url string, client *http.Client) (*WSTransport, error) {
	return DialWebSocketWithHeaders(ctx, url, client, nil)
}

func DialWebSocketWithHeaders(ctx context.Context, url string, client *http.Client, header http.Header) (*WSTransport, error) {
	opts := &websocket.DialOptions{HTTPClient: client, HTTPHeader: header}
	// coder/websocket nils resp.Body on a successful handshake (the underlying
	// stream becomes the Conn, closed via WSTransport.Close) and reads+closes
	// it itself on failure, so there is no response body for us to close here.
	conn, _, err := websocket.Dial(ctx, url, opts) //nolint:bodyclose // library manages the handshake response body (see comment)
	if err != nil {
		return nil, err
	}
	return NewWSTransport(conn), nil
}

func NewWSTransport(conn *websocket.Conn) *WSTransport {
	return NewObservedWSTransport(conn, nil)
}

// NewObservedWSTransport constructs a transport that reports its raw data
// frames to observer. A nil observer preserves the ordinary transport path.
func NewObservedWSTransport(conn *websocket.Conn, observer FrameObserver) *WSTransport {
	conn.SetReadLimit(appWireWebSocketReadLimit)
	return &WSTransport{conn: conn, rec: appwireFrameRecorder, observer: observer}
}

func (t *WSTransport) Send(ctx context.Context, msg Message) error {
	data, err := marshalWSMessage(msg)
	if err != nil {
		return err
	}
	t.rec.RecordSend(data)
	writeCtx, cancelWrite := frameWriteContext(ctx)
	err = writeWebSocket(t.conn, writeCtx, websocket.MessageText, data)
	cancelWrite()
	if err != nil {
		return err
	}
	if t.observer != nil {
		t.observer.RecordSend(data)
	}
	return nil
}

func (t *WSTransport) Recv(ctx context.Context) (Message, error) {
	msg, _, err := t.recvBound(ctx, nil)
	return msg, err
}

// recvBound reads one frame and decodes it through decodeFrameBound, so a large
// response's result is decoded straight into the storage bind resolves for its
// id instead of being scanned a second time by the client.
func (t *WSTransport) recvBound(ctx context.Context, bind frameBinder) (Message, any, error) {
	_, data, err := readWebSocket(t.conn, ctx)
	if err != nil {
		return Message{}, nil, err
	}
	t.rec.RecordRecv(data)
	if t.observer != nil {
		t.observer.RecordRecv(data)
	}
	return decodeFrameBound(data, bind)
}

// Ping implements Pinger: it sends a WebSocket ping and blocks until the peer
// pongs or ctx is done. The client keepalive loop uses it to detect a
// silently-dropped connection.
func (t *WSTransport) Ping(ctx context.Context) error {
	return pingWebSocket(t.conn, ctx)
}

func (t *WSTransport) Close() error {
	return t.conn.Close(websocket.StatusNormalClosure, "")
}
