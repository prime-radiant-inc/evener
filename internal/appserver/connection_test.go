package appserver

import (
	"context"
	"testing"
)

// TestNotify pins that a handler's notification reaches the connection its
// context carries, and that a context without a usable connection, or an
// empty method, sends nothing and does not panic.
func TestNotify(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-notify")
	server.registerConnection(conn)
	withConn := context.WithValue(context.Background(), connectionContextKey{}, conn)
	for _, tc := range []struct {
		name         string
		ctx          context.Context
		method       string
		wantEnqueued bool
	}{
		{name: "connection in context", ctx: withConn, method: "test/method", wantEnqueued: true},
		{name: "empty method", ctx: withConn, method: ""},
		{name: "no connection in context", ctx: context.Background(), method: "test/method"},
		{name: "nil connection in context", ctx: context.WithValue(context.Background(), connectionContextKey{}, (*Connection)(nil)), method: "test/method"},
		{name: "context value is not a connection", ctx: context.WithValue(context.Background(), connectionContextKey{}, "not a connection"), method: "test/method"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			Notify(tc.ctx, tc.method, map[string]string{"key": "value"})
			select {
			case msg := <-conn.send:
				if !tc.wantEnqueued {
					t.Fatalf("Notify enqueued %+v, want nothing", msg)
				}
				if msg.Notification == nil || msg.Notification.Method != tc.method {
					t.Fatalf("Notify enqueued %+v, want a %q notification", msg, tc.method)
				}
			default:
				if tc.wantEnqueued {
					t.Fatal("Notify enqueued nothing for the context's connection")
				}
			}
		})
	}
}
