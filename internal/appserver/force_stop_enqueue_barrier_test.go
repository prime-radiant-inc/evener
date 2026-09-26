package appserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// parkSendLoopLeavingRoom mirrors parkSendLoopWithFullBuffer (which fills the
// outbound buffer to capacity) but stops once exactly room slots remain
// free. The backpressure test needs two: one for the first force stop's own
// response, so its enqueue does not itself block (only beforeSend's clear
// waits behind the backlog), and one for the second force stop's response,
// so that response landing in the channel is an observable, race-free signal
// that the server has already decided its fate.
func parkSendLoopLeavingRoom(t *testing.T, server *Server, gate *gatedSendTransport, room int) *Connection {
	t.Helper()
	conn := registeredConnection(t, server)
	gate.gated.Store(true)
	if !conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
		t.Fatal("could not enqueue the first fill notification")
	}
	waitFor(t, "send loop to park in the gated Send", gate.blocked)
	for cap(conn.send)-len(conn.send) > room {
		if !conn.enqueue(appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, struct{}{})) {
			t.Fatalf("outbound buffer filled before reaching the target of %d free slots", room)
		}
	}
	return conn
}

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

// TestHandleRecoveredCoversPanicFromRecoveryPathOnResponse pins a roborev
// finding against this fix's first version: handleRecovered's own recover
// branch (taken when HandleMessage itself panics) called onResponse directly,
// with no protection of its own. If that call panicked too — the exact
// enqueue-panic failure mode the barrier exists to contain — the second
// panic replaced the first mid-recover and propagated out of handleRecovered
// uncaught, since nothing above it in the force-stop dispatch goroutine (or
// runWorker, for the inline ping path) recovers a second time. An unrecovered
// panic in any goroutine crashes the whole process.
//
// handleRecovered now routes every onResponse call — the normal-path call and
// the synthesized-error call from the recover branch — through one small
// closure that recovers its own panics, so a panic from onResponse can never
// re-enter (and escape) the outer barrier.
func TestHandleRecoveredCoversPanicFromRecoveryPathOnResponse(t *testing.T) {
	server, logged := captureLogfServer()
	HandleTyped(server.Router(), "test/handlerPanics", func(context.Context, appwire.EmptyParams) (appwire.EmptyResponse, error) {
		panic("handler blew up")
	})
	conn := server.NewConnection("c1")
	conn.setInitialized()
	msg := rawRequest(t, 11, "test/handlerPanics", appwire.EmptyParams{})

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped handleRecovered: %v", r)
			}
		}()
		conn.handleRecovered(context.Background(), msg, func(appwire.Message) {
			panic("onResponse blew up delivering the synthesized error")
		})
	}()

	output := logged()
	for _, want := range []string{
		"panic handling test/handlerPanics",
		"handler blew up",
		"onResponse blew up delivering the synthesized error",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("panic log missing %q in:\n%s", want, output)
		}
	}
}

// TestClearRecoveryOnPanicRollsBackBusyFlagBeforeRepanicking pins a second
// roborev finding: markRecoveryClear runs before enqueueDispatched, so if
// enqueueDispatched panics before the response ever reaches the send
// channel, beforeSend — the only thing that clears recoveryRunning and
// recoveryClearID — never runs for it, and the connection would be stuck
// refusing every future force stop.
//
// clearRecoveryOnPanic wraps the force-stop dispatch's enqueue step: on a
// panic, it rolls the busy flag back itself (best effort, since nothing else
// ever will) before letting the panic continue up to handleRecovered's own
// barrier, which still logs it and — since a request ID is present — this
// wraps the same onResponse continuation, so no follow-up response is
// expected; the flag rollback is what matters here.
func TestClearRecoveryOnPanicRollsBackBusyFlagBeforeRepanicking(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	conn := server.NewConnection("c1")
	conn.mu.Lock()
	conn.recoveryRunning = true
	conn.recoveryClearID = "some-response-id"
	conn.mu.Unlock()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("clearRecoveryOnPanic swallowed the panic instead of letting it continue to the caller's own barrier")
			}
		}()
		conn.clearRecoveryOnPanic(func() { panic("enqueue blew up before reaching the send channel") })
	}()

	conn.mu.RLock()
	defer conn.mu.RUnlock()
	if conn.recoveryRunning || conn.recoveryClearID != "" {
		t.Fatal("busy flag/clear id were not rolled back before the panic propagated: the connection would refuse every future force stop")
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
// This test gates the outbound transport and fills the send buffer, leaving
// exactly two slots free, so the first force stop's response enqueue
// genuinely blocks behind the backlog (a full-minus-two channel with a
// parked drain side blocks unconditionally once those two slots fill — this
// isn't timing-dependent), then asserts a second, concurrent force stop is
// refused while the first is still stuck, and that releasing the transport
// lets the first complete and a subsequent force stop succeed again.
//
// The two free slots are the test's synchronization point (a roborev finding
// against an earlier version of this test, which sent the second force stop
// and released the transport gate immediately, racing the gate release
// against the receive loop's own scheduling: nothing confirmed the second
// force stop had actually been decided before the drain could clear
// recoveryRunning out from under it). The first slot is the first force
// stop's own response, whose enqueue can complete without blocking (the
// backpressure under test is beforeSend not having run yet, not the enqueue
// itself); the second is whatever response the second force stop
// produces — a busy refusal if the flag correctly held, or a real success if
// it didn't. Either way, that response can only be sitting in the channel
// once the server has fully decided the second force stop's fate, so waiting
// for the buffer to fill again is a race-free barrier: it happens before the
// gate is released, and only afterward do we drain the frames and assert
// which outcome occurred.
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
	conn := parkSendLoopLeavingRoom(t, server, gate, 2)
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
	waitUntil(t, "the second force stop's response to be enqueued (proving the server decided its fate before the transport gate is released)", func() bool {
		return len(conn.send) == cap(conn.send)
	})

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
