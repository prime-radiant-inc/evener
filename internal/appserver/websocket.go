package appserver

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
)

var serverConnSeq atomic.Uint64

const (
	webSocketWriteTimeout = 30 * time.Second

	// keepalivePingInterval / keepalivePongTimeout bound silent-drop
	// detection. coder/websocket does not auto-ping, so without this a TCP
	// connection that dies without a close frame (sleep/wake, NAT rebind,
	// proxy idle timeout) leaves the recv loop blocked forever and the peer
	// desynced until a manual reconnect. We ping the peer on an interval and
	// tear the connection down if it does not pong within the timeout, which
	// surfaces to the recv loop as a normal error and drives reconnect.
	keepalivePingInterval = 15 * time.Second
	keepalivePongTimeout  = 10 * time.Second
)

type webSocketSender interface {
	Send(context.Context, appwire.Message) error
}

type webSocketTransport interface {
	webSocketSender
	Recv(context.Context) (appwire.Message, error)
}

type webSocketCloser interface {
	Close(websocket.StatusCode, string) error
}

// wsPinger is the subset of *websocket.Conn the keepalive loop needs. Ping
// sends a WebSocket ping and blocks until the peer pongs or ctx is done.
type wsPinger interface {
	Ping(context.Context) error
}

type webSocketPingAttempt struct {
	cancel   context.CancelFunc
	deferred bool
}

type webSocketKeepaliveTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type realWebSocketKeepaliveTicker struct {
	ticker *time.Ticker
}

func (t realWebSocketKeepaliveTicker) Chan() <-chan time.Time { return t.ticker.C }
func (t realWebSocketKeepaliveTicker) Stop()                  { t.ticker.Stop() }

func newRealWebSocketKeepaliveTicker(interval time.Duration) webSocketKeepaliveTicker {
	return realWebSocketKeepaliveTicker{ticker: time.NewTicker(interval)}
}

type webSocketReadGate struct {
	mu        sync.Mutex
	available bool
	active    *webSocketPingAttempt
}

func newWebSocketReadGate() *webSocketReadGate {
	return &webSocketReadGate{available: true}
}

func (g *webSocketReadGate) readerAvailable() {
	g.mu.Lock()
	g.available = true
	g.mu.Unlock()
}

func (g *webSocketReadGate) readerUnavailable() {
	g.mu.Lock()
	g.available = false
	if g.active != nil {
		g.active.deferred = true
		g.active.cancel()
	}
	g.mu.Unlock()
}

func (g *webSocketReadGate) beginPing(parent context.Context) (context.Context, *webSocketPingAttempt, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.available {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	attempt := &webSocketPingAttempt{cancel: cancel}
	g.active = attempt
	return ctx, attempt, true
}

func (g *webSocketReadGate) finishPing(attempt *webSocketPingAttempt) bool {
	g.mu.Lock()
	deferred := attempt.deferred
	if g.active == attempt {
		g.active = nil
	}
	g.mu.Unlock()
	attempt.cancel()
	return deferred
}

func (s *Server) ServeWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.beginWebSocket() {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	defer s.endWebSocket()

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // connection cleanup; close error is not actionable

	connectionID := fmt.Sprintf("conn-%d", serverConnSeq.Add(1))
	var observer appwire.FrameObserver
	if s.cfg.WebSocketTrace != nil {
		s.cfg.WebSocketTrace.ConnectionOpened(connectionID)
		defer s.cfg.WebSocketTrace.ConnectionClosed(connectionID, nil)
		observer = s.cfg.WebSocketTrace.Observer(connectionID)
	}
	transport := webSocketTransport(appwire.NewObservedWSTransport(ws, observer))
	if s.wrapWebSocketTransport != nil {
		transport = s.wrapWebSocketTransport(transport)
	}
	ctx, cancel := context.WithCancel(r.Context())
	conn := s.NewConnection(connectionID)
	conn.setCancel(cancel)
	s.registerConnection(conn)
	if s.webSocketShutdownStarted() {
		cancel()
	}
	defer func() {
		cancel()
		s.unregisterConnection(conn)
	}()

	writeTimeout := s.sendWriteTimeout
	if writeTimeout == 0 {
		writeTimeout = webSocketWriteTimeout
	}
	var transportLoops sync.WaitGroup
	transportLoops.Go(func() {
		defer cancel()
		runWebSocketSendLoopWithTimeout(ctx, transport, conn.send, writeTimeout)
	})
	go conn.runWorker(ctx)

	gate := newWebSocketReadGate()
	transportLoops.Go(func() {
		runWebSocketKeepaliveWithTicker(ctx, ws, cancel, gate, keepalivePongTimeout, s.keepaliveTickerFactory(keepalivePingInterval), s.keepaliveDecision)
	})

	runWebSocketReceiveLoop(ctx, ws, transport, conn, gate)
	// The receive loop returned, so the queue's only producer has stopped —
	// cancel first so a racing worker dequeue discards rather than executes,
	// then drop the buffered frames (see purgeRequestQueue). The deferred
	// cancel/unregister above still runs afterward; cancel is idempotent.
	cancel()
	conn.purgeRequestQueue()
	transportLoops.Wait()
}

// runWebSocketReceiveLoop keeps only transport concerns: park in Recv
// (reader available; keepalive can ping), hand each frame to the
// connection's inbound policy (Connection.receiveInbound owns the
// ping-inline-vs-enqueue decision), and stop when the transport or the
// connection dies.
func runWebSocketReceiveLoop(ctx context.Context, ws webSocketCloser, transport webSocketTransport, conn *Connection, gate *webSocketReadGate) {
	for {
		gate.readerAvailable()
		msg, err := transport.Recv(ctx)
		gate.readerUnavailable()
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				_ = ws.Close(websocket.StatusInternalError, err.Error())
			}
			return
		}
		if !conn.receiveInbound(ctx, msg) {
			return
		}
	}
}

// runWebSocketKeepalive pings the peer every interval and cancels the
// connection if a ping is not answered within timeout. A dead peer thus
// surfaces to the recv/send loops as context cancellation rather than an
// indefinite block.
func runWebSocketKeepalive(ctx context.Context, conn wsPinger, cancel context.CancelFunc, gate *webSocketReadGate, interval, timeout time.Duration) {
	runWebSocketKeepaliveWithTicker(ctx, conn, cancel, gate, timeout, realWebSocketKeepaliveTicker{ticker: time.NewTicker(interval)}, nil)
}

func runWebSocketKeepaliveWithTicker(ctx context.Context, conn wsPinger, cancel context.CancelFunc, gate *webSocketReadGate, timeout time.Duration, ticker webSocketKeepaliveTicker, onDecision func(bool)) {
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			attemptCtx, attempt, ok := gate.beginPing(ctx)
			if onDecision != nil {
				onDecision(ok)
			}
			if !ok {
				continue
			}
			pingCtx, pingCancel := context.WithTimeout(attemptCtx, timeout)
			err := conn.Ping(pingCtx)
			pingCancel()
			deferred := gate.finishPing(attempt)
			if err != nil && !deferred {
				cancel()
				return
			}
		}
	}
}

func runWebSocketSendLoop(ctx context.Context, transport webSocketSender, send <-chan appwire.Message) {
	runWebSocketSendLoopWithTimeout(ctx, transport, send, webSocketWriteTimeout)
}

func runWebSocketSendLoopWithTimeout(ctx context.Context, transport webSocketSender, send <-chan appwire.Message, writeTimeout time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-send:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := transport.Send(writeCtx, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
