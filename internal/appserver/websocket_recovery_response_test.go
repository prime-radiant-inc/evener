package appserver

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestServeWebSocketForceStopClearsRecoveryBeforeResponseIsSent pins the fix
// for a production race: recoveryRunning, the guard that refuses a second
// concurrent force-stop request on a connection, used to clear in a defer
// once the force-stop handler goroutine returned. That raced the same
// response's delivery to the client — the send loop still had to dequeue and
// transmit the frame, and nothing ordered that against the goroutine's
// trivial deferred cleanup — so a client sending a second, fully sequential
// force stop right behind the first could have it arrive and be checked
// before the defer ran, and be refused with "force stop is already running"
// for a recovery that had already finished
// (TestDaemonActionRefusesStaleRenderedIdentity's flake).
//
// The fix clears the flag before the response is enqueued rather than after
// the handler goroutine returns, which gives a real ordering guarantee
// instead of a race: a channel send happens before the corresponding receive
// completes, so the send loop can never dequeue (and so never transmit) the
// force-stop response before the flag is already false. This test proves
// exactly that ordering, deterministically: it gates the transport so the
// send loop parks the instant it tries to write the force-stop response, and
// asserts recoveryRunning is already false at that point — before the
// client could possibly have received the response and issued another
// request.
func TestServeWebSocketForceStopClearsRecoveryBeforeResponseIsSent(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	gate := &gatedSendTransport{blocked: make(chan struct{}, 1), release: make(chan struct{})}
	server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
		gate.webSocketTransport = inner
		return gate
	}
	HandleTyped(server.Router(), appwire.MethodEvenerThreadForceStop, func(context.Context, appwire.ThreadForceStopParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	httpServer := serveWebSocketHTTP(t, server)
	transport := dialRawAppWire(t, httpServer)
	initializeRaw(t, transport)
	conn := registeredConnection(t, server)

	gate.gated.Store(true)
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}))
	waitFor(t, "send loop to park writing the force-stop response", gate.blocked)

	conn.mu.RLock()
	busy := conn.recoveryRunning
	conn.mu.RUnlock()
	if busy {
		t.Fatal("recoveryRunning was still true while the force-stop response was gated for delivery: a client that received this response and immediately sent another force stop would have been wrongly refused")
	}
	close(gate.release)
}
