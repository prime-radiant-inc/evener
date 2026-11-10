package sshconn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
)

// The SSH process is modeled, but bearer rejection, the WebSocket handshake and
// the AppWire initialize/read exchanges execute through production transports.
func completedRestartAuthenticatedBridge(t *testing.T, fr *fakeRunner) *atomic.Int32 {
	t.Helper()
	var reads atomic.Int32
	const token = "completed-restart-test-only"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		transport := appwire.NewWSTransport(conn)
		for {
			msg, err := transport.Recv(r.Context())
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			var result any
			switch msg.Request.Method {
			case "initialize":
				result = appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}
			case "thread/read":
				reads.Add(1)
				result = map[string]any{"thread": appwire.Thread{ID: "read-proof", SessionID: "read-proof", Name: "completed-successor"}}
			default:
				t.Errorf("unexpected authenticated method %q", msg.Request.Method)
				return
			}
			if err := transport.Send(r.Context(), appwire.Message{Response: &appwire.Response{ID: msg.Request.ID, Result: result}}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	if conn, err := appwire.DialWebSocket(t.Context(), url, nil); err == nil {
		_ = conn.Close()
		t.Fatal("unauthenticated bridge was accepted")
	}
	fr.startFn = func(ctx context.Context, _ []string, _ io.Writer) (Stdio, error) {
		remote, err := appwire.DialWebSocketWithHeaders(ctx, url, nil, http.Header{"Authorization": {"Bearer " + token}})
		if err != nil {
			return nil, err
		}
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		stdio := &fakeStdio{inW: inW, inR: inR, outR: outR, outW: outW, waitDone: make(chan struct{})}
		stream := appwire.NewStreamTransport(&completedRestartStream{Reader: inR, Writer: outW})
		t.Cleanup(func() { _ = stdio.Kill(); _ = remote.Close() })
		go func() {
			defer stdio.Kill()
			for {
				msg, err := stream.Recv(ctx)
				if err != nil {
					return
				}
				if err := remote.Send(ctx, msg); err != nil {
					return
				}
			}
		}()
		go func() {
			defer stdio.Kill()
			for {
				msg, err := remote.Recv(ctx)
				if err != nil {
					return
				}
				if err := stream.Send(ctx, msg); err != nil {
					return
				}
			}
		}()
		return stdio, nil
	}
	return &reads
}

type completedRestartStream struct {
	io.Reader
	io.Writer
}

func (*completedRestartStream) Close() error { return nil }
