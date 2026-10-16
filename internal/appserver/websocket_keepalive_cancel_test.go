package appserver

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
)

// keepaliveCancelConn holds one real Ping inside net.Conn.Write. Close must
// unblock Write too: coder/websocket closes the transport to interrupt a write
// whose context was canceled. No WebSocket operation or frame is mocked.
type keepaliveCancelConn struct {
	net.Conn
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	unblock func()
	onClose sync.Once
}

func (c *keepaliveCancelConn) Write(p []byte) (int, error) {
	// The server is uncompressed and unmasked. A Ping's FIN/opcode byte is
	// 0x89, and its short frame is flushed in one write by coder/websocket.
	if len(p) > 0 && p[0] == 0x89 && c.armed.CompareAndSwap(true, false) {
		close(c.entered)
		select {
		case <-c.release:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *keepaliveCancelConn) Close() error {
	err := c.Conn.Close()
	c.onClose.Do(func() { close(c.closed) })
	return err
}

type keepaliveCancelListener struct {
	net.Listener
	accepted chan *keepaliveCancelConn
}

func (l *keepaliveCancelListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &keepaliveCancelConn{
		Conn: conn, entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}),
	}
	wrapped.unblock = sync.OnceFunc(func() { close(wrapped.release) })
	l.accepted <- wrapped
	return wrapped, nil
}

// Observe entry into the next real Recv, after runWebSocketReceiveLoop has
// called readerUnavailable for the preceding frame. This adds no transport
// behavior, and avoids racing the test's Ping against readerAvailable.
type keepaliveCancelReadObserver struct {
	webSocketTransport
	reads    chan string
	previous string
}

func (o *keepaliveCancelReadObserver) Recv(ctx context.Context) (appwire.Message, error) {
	o.reads <- o.previous
	msg, err := o.webSocketTransport.Recv(ctx)
	o.previous = ""
	if msg.Request != nil {
		o.previous = msg.Request.Method
	}
	return msg, err
}

// Inbound traffic must not cancel a pending resume by interrupting a native
// Ping write. The real server loops and WebSocket share an exposed read gate
// so the test can observe the Ping context. That observation lets us await
// socket closure after cancellation without releasing the write too early
// or guessing a timing window.
func TestWebSocketReadGateInboundFrameDuringPingWriteKeepsConnectionOpen(t *testing.T) {
	t.Parallel()
	server := NewServer(ServerConfig{ServerName: "keepalive-cancel-test", Version: "test", SourceID: "local"})
	resumeStarted := make(chan struct{})
	resumeCanceled := make(chan error, 1)
	resumeRelease := make(chan struct{})
	releaseResume := sync.OnceFunc(func() { close(resumeRelease) })
	HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, _ appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		close(resumeStarted)
		select {
		case <-ctx.Done():
			resumeCanceled <- ctx.Err()
			return appwire.ThreadResumeResponse{}, ctx.Err()
		case <-resumeRelease:
			return appwire.ThreadResumeResponse{Thread: appwire.Thread{ID: "th_held"}}, nil
		}
	})

	gate := newWebSocketReadGate()
	reads := make(chan string, 8)
	type acceptedWebSocket struct {
		ws  *websocket.Conn
		ctx context.Context
	}
	accepted := make(chan acceptedWebSocket, 1)
	serverDone := make(chan struct{})
	httpServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer ws.CloseNow() //nolint:errcheck // test cleanup
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		conn := server.NewConnection("keepalive-cancel")
		conn.setCancel(cancel)
		transport := &keepaliveCancelReadObserver{webSocketTransport: appwire.NewWSTransport(ws), reads: reads}
		var loops sync.WaitGroup
		loops.Go(func() {
			defer cancel()
			runWebSocketSendLoop(ctx, transport, conn.send)
		})
		loops.Go(func() { conn.runWorker(ctx) })
		accepted <- acceptedWebSocket{ws: ws, ctx: ctx}
		runWebSocketReceiveLoop(ctx, ws, transport, conn, gate)
		cancel()
		conn.purgeRequestQueue()
		loops.Wait()
	}))
	listener := &keepaliveCancelListener{Listener: httpServer.Listener, accepted: make(chan *keepaliveCancelConn, 1)}
	httpServer.Listener = listener
	httpServer.Start()
	t.Cleanup(httpServer.Close)

	ws, _, err := websocket.Dial(t.Context(), "ws"+httpServer.URL[len("http"):], &websocket.DialOptions{HTTPClient: httpServer.Client()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	netConn := waitFor(t, "accepted underlying connection", listener.accepted)
	peer := waitFor(t, "accepted WebSocket", accepted)
	client := appwire.NewClient(appwire.NewWSTransport(ws))
	client.Start(t.Context())
	t.Cleanup(func() {
		netConn.unblock()
		releaseResume()
		_ = ws.CloseNow()
		_ = peer.ws.CloseNow()
		waitFor(t, "server loops to exit", serverDone)
	})
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	resumeDone := make(chan error, 1)
	go func() {
		var out appwire.ThreadResumeResponse
		resumeDone <- client.Request(t.Context(), appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: "local:th_held"}, &out)
	}()
	waitFor(t, "resume handler to park", resumeStarted)
	for waitFor(t, "receive loop after resume intake", reads) != appwire.MethodThreadResume {
	}
	// Establish that this peer really reads and answers native control frames
	// before placing the next Ping inside the controlled write window.
	if err := peer.ws.Ping(t.Context()); err != nil {
		t.Fatalf("healthy-peer control Ping: %v", err)
	}

	netConn.armed.Store(true)
	attemptCtx, attempt, ok := gate.beginPing(peer.ctx)
	if !ok {
		t.Fatal("receive loop did not leave its reader available")
	}
	t.Cleanup(attempt.cancel)
	pingCtx, pingCancel := context.WithTimeout(attemptCtx, keepalivePongTimeout)
	t.Cleanup(pingCancel)
	type pingResult struct {
		err      error
		deferred bool
	}
	pingDone := make(chan pingResult, 1)
	go func() {
		err := peer.ws.Ping(pingCtx)
		pingCancel()
		pingDone <- pingResult{err: err, deferred: gate.finishPing(attempt)}
	}()
	waitFor(t, "real native Ping to enter net.Conn.Write", netConn.entered)

	intakeDone := make(chan error, 1)
	go func() {
		var out appwire.EmptyResponse
		intakeDone <- client.Request(t.Context(), appwire.MethodPing, appwire.EmptyParams{}, &out)
	}()
	// receiveInbound handles app-level ping inline even while resume is parked.
	// Its next Recv proves readerUnavailable ran while native Ping was writing.
	if got := waitFor(t, "receive loop after application Ping intake", reads); got != appwire.MethodPing {
		t.Fatalf("preceding request = %q, want %q", got, appwire.MethodPing)
	}
	if attemptCtx.Err() != nil {
		// A canceled write context is a positive signal, not a timeout-based
		// absence check. Keep Write blocked until coder/websocket itself closes
		// the real net.Conn, then observe the parked request's cancellation.
		waitFor(t, "socket closure after read-gate cancellation", netConn.closed)
		resumeErr := waitFor(t, "parked resume cancellation", resumeCanceled)
		ping := waitFor(t, "native Ping to return", pingDone)
		intakeErr := waitFor(t, "application Ping to return", intakeDone)
		waitFor(t, "resume client request to return", resumeDone)
		t.Fatalf("inbound application frame closed a healthy connection while native Ping was writing: attempt=%v, deferred=%t, native Ping=%v, parked resume=%v, application Ping=%v", attemptCtx.Err(), ping.deferred, ping.err, resumeErr, intakeErr)
	}

	// On a non-canceling gate, release the write and require both real Ping
	// and application traffic to complete, not merely an unobserved close.
	netConn.unblock()
	if ping := waitFor(t, "native Ping to complete", pingDone); ping.err != nil {
		t.Fatalf("native Ping after intake: %v", ping.err)
	}
	if err := waitFor(t, "application Ping response", intakeDone); err != nil {
		t.Fatalf("application Ping after intake: %v", err)
	}
	releaseResume()
	if err := waitFor(t, "resume response", resumeDone); err != nil {
		t.Fatalf("resume after intake: %v", err)
	}
}
