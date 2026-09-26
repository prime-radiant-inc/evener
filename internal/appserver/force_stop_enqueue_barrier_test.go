package appserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// TestHandleRecoveredCoversOnResponsePanic pins a roborev finding against
// #2469: splitting handleAndEnqueue into handleRecovered (a panic barrier
// around HandleMessage) plus a separate enqueueDispatched call narrowed the
// barrier. Before the split, one function ran the handler and enqueued its
// response under a single defer/recover; after, enqueueDispatched ran as a
// second, unwrapped call, so a panic during enqueue — not just during the
// handler — could escape the goroutine and crash the process instead of
// answering InternalError.
//
// handleRecovered now takes the enqueue step as a continuation (onResponse)
// invoked from inside its own defer/recover, so a panic from that
// continuation is covered exactly like a panic from HandleMessage itself.
// This test calls onResponse in a way that panics only once success is
// already in hand — mirroring a panic inside enqueueDispatched after
// HandleMessage returned cleanly — and asserts the panic never escapes the
// call and is logged like any other handler panic.
func TestHandleRecoveredCoversOnResponsePanic(t *testing.T) {
	server, logged := captureLogfServer()
	conn := server.NewConnection("c1")
	msg := rawRequest(t, 9, "test/onResponsePanic", appwire.EmptyParams{})

	var calls int
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped handleRecovered: %v", r)
			}
		}()
		conn.handleRecovered(context.Background(), msg, func(appwire.Message) {
			calls++
			panic("boom during enqueue")
		})
	}()

	if calls != 1 {
		t.Fatalf("onResponse called %d times, want exactly 1 (no duplicate response after the panic)", calls)
	}
	output := logged()
	if !strings.Contains(output, "panic handling test/onResponsePanic") || !strings.Contains(output, "boom during enqueue") {
		t.Fatalf("panic log missing expected content:\n%s", output)
	}
}

// TestServeWebSocketForceStopBackpressureHoldsAcrossEnqueue pins the second
// roborev finding against #2469: the fix that clears recoveryRunning before
// the response is enqueued (rather than after the handler goroutine returns)
// also stopped bounding force stops to one in flight. enqueueResponse can
// block for a long time against a peer that has stopped draining its send
// channel, and with the flag cleared before that block, a second force stop
// arriving while the first's response is still stuck in the outbound buffer
// would have been admitted instead of refused — no client-visible bound on
// how many force-stop dispatch goroutines can pile up.
//
// The fix keeps recoveryRunning true across the whole enqueue and clears it
// only in the send loop, immediately before the marked response is handed to
// the transport (beforeSend), preserving the ordering guarantee the original
// fix established (the clear still strictly precedes transmission) while
// restoring the one-at-a-time bound.
//
// This test gates the outbound transport and fills the send buffer so the
// first force stop's response enqueue genuinely blocks (a full channel with a
// parked drain side blocks unconditionally — this isn't timing-dependent),
// then asserts a second, concurrent force stop is refused while the first is
// still stuck, and that releasing the transport lets the first complete and
// a subsequent force stop succeed again.
func TestServeWebSocketForceStopBackpressureHoldsAcrossEnqueue(t *testing.T) {
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
	conn := parkSendLoopWithFullBuffer(t, server, gate)
	frames := collectFrames(transport)

	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}))
	waitUntil(t, "the first force stop to mark its response for a send-loop clear", func() bool {
		conn.mu.RLock()
		defer conn.mu.RUnlock()
		return conn.recoveryClearID != ""
	})
	conn.mu.RLock()
	busy := conn.recoveryRunning
	conn.mu.RUnlock()
	if !busy {
		t.Fatal("recoveryRunning was already false while the first force stop's response was still stuck behind a full, undrained outbound buffer: backpressure was lost")
	}

	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}))

	close(gate.release)
	var sawBusyRefusal, sawFirstSuccess bool
	deadline := time.After(5 * time.Second)
	for !sawBusyRefusal || !sawFirstSuccess {
		select {
		case msg := <-frames:
			switch {
			case msg.Error != nil && msg.Error.ID.Int64() == 3:
				if msg.Error.Error.Code != appwire.CodeUnavailable {
					t.Fatalf("second force stop error code = %d, want CodeUnavailable", msg.Error.Error.Code)
				}
				sawBusyRefusal = true
			case msg.Response != nil && msg.Response.ID.Int64() == 2:
				sawFirstSuccess = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for both frames: busyRefusal=%v firstSuccess=%v", sawBusyRefusal, sawFirstSuccess)
		}
	}

	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}))
	deadline = time.After(5 * time.Second)
	for {
		select {
		case msg := <-frames:
			if msg.Response != nil && msg.Response.ID.Int64() == 4 {
				return
			}
			if msg.Error != nil && msg.Error.ID.Int64() == 4 {
				t.Fatalf("third force stop, sent after the first's response drained, was refused: %+v", msg.Error)
			}
		case <-deadline:
			t.Fatal("timed out waiting for the third force stop to succeed once recoveryRunning cleared")
		}
	}
}
