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

const webSocketWriteTimeout = 30 * time.Second

type webSocketSender interface {
	Send(context.Context, appwire.Message) error
}

func (s *Server) ServeWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

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
