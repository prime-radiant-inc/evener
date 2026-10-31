package appserver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"primeradiant.com/evener/appwire"
)

// NewInProcessTransport connects an internal AppWire client to the same
// admission, dispatch, subscription handoff and bounded outbound queue as a
// WebSocket client. The caller must initialize normally and close the transport.
func (s *Server) NewInProcessTransport(parent context.Context) (appwire.Transport, error) {
	// Share the shutdown admission/count with socket handlers so Shutdown waits
	// for both kinds of connection to withdraw their subscriptions.
	if !s.beginWebSocket() {
		return nil, errors.New("server is shutting down")
	}
	if s.cfg.ConnectionAdmissionContext != nil {
		parent = s.cfg.ConnectionAdmissionContext(parent)
	}
	ctx, cancel := context.WithCancel(parent)
	conn := s.NewConnection(fmt.Sprintf("conn-%d", serverConnSeq.Add(1)))
	conn.sessionLifetime = ctx
	conn.setCancel(cancel)
	s.registerConnection(conn)
	if s.webSocketShutdownStarted() {
		cancel()
	}
	transport := &inProcessTransport{conn: conn, ctx: ctx, cancel: cancel, closed: make(chan struct{})}
	go conn.runWorker(ctx)
	go func() {
		<-ctx.Done()
		// Stop the sole inbound producer before purging, just as the socket
		// receive loop does on exit. Cancellation prevents queued execution.
		transport.sendMu.Lock()
		conn.purgeRequestQueue()
		transport.sendMu.Unlock()
		conn.closeSessionTools()
		s.unregisterConnection(conn)
		conn.runPendingAfterWrite()
		s.endWebSocket()
		close(transport.closed)
	}()
	return transport, nil
}

type inProcessTransport struct {
	conn   *Connection
	ctx    context.Context
	cancel context.CancelFunc
	sendMu sync.Mutex
	closed chan struct{}
}

func (t *inProcessTransport) Send(ctx context.Context, msg appwire.Message) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.ctx.Err(); err != nil {
		return err
	}
	// A caller's request deadline does not own the connection or an admitted
	// handler, matching the socket receive loop's connection-lifetime context.
	if !t.conn.receiveInbound(t.ctx, msg) {
		t.cancel()
		return context.Canceled
	}
	return nil
}

func (t *inProcessTransport) Recv(ctx context.Context) (appwire.Message, error) {
	if err := t.ctx.Err(); err != nil {
		return appwire.Message{}, err
	}
	select {
	case <-ctx.Done():
		return appwire.Message{}, ctx.Err()
	case <-t.ctx.Done():
		return appwire.Message{}, t.ctx.Err()
	case msg, ok := <-t.conn.send:
		if !ok {
			return appwire.Message{}, context.Canceled
		}
		t.conn.beforeSend(msg)
		t.conn.afterSend(msg)
		t.conn.responseWritten(msg)
		return msg, nil
	}
}

func (t *inProcessTransport) Close() error {
	t.cancel()
	<-t.closed
	return nil
}
