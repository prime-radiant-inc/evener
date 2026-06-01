package appwire

import (
	"context"
	"encoding/json"
	"net/http"

	"nhooyr.io/websocket"
)

type WSTransport struct {
	conn *websocket.Conn
}

const appWireWebSocketReadLimit = 128 << 20

func DialWebSocket(ctx context.Context, url string, client *http.Client) (*WSTransport, error) {
	return DialWebSocketWithHeaders(ctx, url, client, nil)
}

func DialWebSocketWithHeaders(ctx context.Context, url string, client *http.Client, header http.Header) (*WSTransport, error) {
	opts := &websocket.DialOptions{HTTPClient: client, HTTPHeader: header}
	conn, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, err
	}
	return NewWSTransport(conn), nil
}

func NewWSTransport(conn *websocket.Conn) *WSTransport {
	conn.SetReadLimit(appWireWebSocketReadLimit)
	return &WSTransport{conn: conn}
}

func (t *WSTransport) Send(ctx context.Context, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return t.conn.Write(ctx, websocket.MessageText, data)
}

func (t *WSTransport) Recv(ctx context.Context) (Message, error) {
	_, data, err := t.conn.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

func (t *WSTransport) Close() error {
	return t.conn.Close(websocket.StatusNormalClosure, "")
}
