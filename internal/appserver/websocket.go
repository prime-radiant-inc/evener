package appserver

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/serf/appwire"
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

// wsPinger is the subset of *websocket.Conn the keepalive loop needs. Ping
// sends a WebSocket ping and blocks until the peer pongs or ctx is done.
type wsPinger interface {
	Ping(context.Context) error
}

func (s *Server) ServeWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // connection cleanup; close error is not actionable

	transport := appwire.NewWSTransport(ws)
	ctx, cancel := context.WithCancel(r.Context())
	conn := s.NewConnection(fmt.Sprintf("conn-%d", serverConnSeq.Add(1)))
	conn.setCancel(cancel)
	s.registerConnection(conn)
	defer func() {
		cancel()
		s.unregisterConnection(conn.ID())
	}()

	go func() {
		defer cancel()
		runWebSocketSendLoop(ctx, transport, conn.send)
	}()

	go runWebSocketKeepalive(ctx, ws, cancel, keepalivePingInterval, keepalivePongTimeout)

	for {
		msg, err := transport.Recv(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				_ = ws.Close(websocket.StatusInternalError, err.Error())
			}
			return
		}
		resp := conn.HandleMessage(ctx, msg)
		if resp.Kind() == appwire.MessageInvalid {
			continue
		}
		if err := conn.enqueueResponse(ctx, resp); err != nil {
			return
		}
	}
}

// runWebSocketKeepalive pings the peer every interval and cancels the
// connection if a ping is not answered within timeout. A dead peer thus
// surfaces to the recv/send loops as context cancellation rather than an
// indefinite block.
func runWebSocketKeepalive(ctx context.Context, conn wsPinger, cancel context.CancelFunc, interval, timeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, timeout)
			err := conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

func runWebSocketSendLoop(ctx context.Context, transport webSocketSender, send <-chan appwire.Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-send:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, webSocketWriteTimeout)
			err := transport.Send(writeCtx, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
