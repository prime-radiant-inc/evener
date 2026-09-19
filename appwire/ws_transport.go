package appwire

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/coder/websocket"
)

var (
	marshalWSMessage   = json.Marshal
	unmarshalWSMessage = json.Unmarshal
	pingWebSocket      = (*websocket.Conn).Ping
	readWebSocket      = (*websocket.Conn).Read
)

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
	limit    int
}

const appWireWebSocketReadLimit = 128 << 20

// PrivateBrokerFrameLimit bounds artifact broker control frames. The broker
// never carries artifact source or results, only bounded identity and authority
// metadata, so it does not need the ordinary AppWire composer's 128 MiB limit.
const PrivateBrokerFrameLimit = 256 << 10

var ErrPrivateFrameTooLarge = errors.New("appwire private websocket: frame exceeds limit")

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

// DialPrivateWebSocketWithHeaders dials an unrecorded, bounded broker channel.
// The caller owns proxy, redirect, Host, Origin, and bearer-token policy through
// client and header; this constructor only enforces the private frame behavior.
func DialPrivateWebSocketWithHeaders(ctx context.Context, url string, client *http.Client, header http.Header) (*WSTransport, error) {
	opts := &websocket.DialOptions{HTTPClient: client, HTTPHeader: header}
	conn, _, err := websocket.Dial(ctx, url, opts) //nolint:bodyclose // library manages the handshake response body
	if err != nil {
		return nil, err
	}
	return NewPrivateWSTransport(conn), nil
}

func NewWSTransport(conn *websocket.Conn) *WSTransport {
	return NewObservedWSTransport(conn, nil)
}

// NewObservedWSTransport constructs a transport that reports its raw data
// frames to observer. A nil observer preserves the ordinary transport path.
func NewObservedWSTransport(conn *websocket.Conn, observer FrameObserver) *WSTransport {
	return newWSTransport(conn, appWireWebSocketReadLimit, appwireFrameRecorder, observer)
}

// NewPrivateWSTransport constructs the artifact broker's transport. Private
// frames are never sent to the process-wide fuzz recorder or an observer.
func NewPrivateWSTransport(conn *websocket.Conn) *WSTransport {
	return newWSTransport(conn, PrivateBrokerFrameLimit, nil, nil)
}

func newWSTransport(conn *websocket.Conn, limit int, recorder *FrameRecorder, observer FrameObserver) *WSTransport {
	conn.SetReadLimit(int64(limit))
	return &WSTransport{conn: conn, rec: recorder, observer: observer, limit: limit}
}

func (t *WSTransport) Send(ctx context.Context, msg Message) error {
	data, err := marshalWSMessage(msg)
	if err != nil {
		return err
	}
	if len(data) > t.limit {
		return ErrPrivateFrameTooLarge
	}
	t.rec.RecordSend(data)
	if err := t.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return err
	}
	if t.observer != nil {
		t.observer.RecordSend(data)
	}
	return nil
}

func (t *WSTransport) Recv(ctx context.Context) (Message, error) {
	_, data, err := readWebSocket(t.conn, ctx)
	if err != nil {
		return Message{}, err
	}
	t.rec.RecordRecv(data)
	if t.observer != nil {
		t.observer.RecordRecv(data)
	}
	var msg Message
	if err := unmarshalWSMessage(data, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
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
