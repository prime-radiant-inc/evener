package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestIsDroppedConn pins the transport-drop classification that gates a lazy
// reconnect. The SDK's own taxonomy surfaces a dropped session as
// mcpsdk.ErrConnectionClosed, but a tool call that races the transport
// teardown can surface the underlying transport error FIRST — before the SDK
// marks the session closed and wraps it. Those raw errors (io.ErrClosedPipe
// from an in-memory or stdio pipe, os.ErrClosed from a real subprocess pipe,
// net.ErrClosed from an sse/http socket) mean exactly the same thing as
// ErrConnectionClosed: the transport is gone, so redial. A ctx cancellation
// or a plain JSON-RPC application error must NOT be classified as a drop.
func TestIsDroppedConn(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sdk ErrConnectionClosed", mcpsdk.ErrConnectionClosed, true},
		{"wrapped ErrConnectionClosed", fmt.Errorf("call: %w", mcpsdk.ErrConnectionClosed), true},
		{"io.ErrClosedPipe", io.ErrClosedPipe, true},
		{"wrapped io.ErrClosedPipe", fmt.Errorf("write: %w", io.ErrClosedPipe), true},
		{"os.ErrClosed", os.ErrClosed, true},
		{"wrapped os.ErrClosed", fmt.Errorf("file: %w", os.ErrClosed), true},
		{"net.ErrClosed", net.ErrClosed, true},
		{"wrapped net.ErrClosed", fmt.Errorf("conn: %w", net.ErrClosed), true},
		{"io.EOF", io.EOF, true},
		{"wrapped io.EOF (jsonrpc read after peer hangup)", fmt.Errorf(`calling "tools/call": %w`, io.EOF), true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"wrapped io.ErrUnexpectedEOF", fmt.Errorf("decode: %w", io.ErrUnexpectedEOF), true},
		{"plain JSON-RPC error", errors.New("boom: rpc-level failure"), false},
		{"ctx canceled", context.Canceled, false},
		{"ctx deadline exceeded", context.DeadlineExceeded, false},
		{"ctx canceled joined with io.EOF stays not-a-drop", errors.Join(context.Canceled, io.EOF), false},
		{"ctx deadline joined with closed pipe stays not-a-drop", errors.Join(context.DeadlineExceeded, io.ErrClosedPipe), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDroppedConn(tc.err); got != tc.want {
				t.Errorf("isDroppedConn(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
