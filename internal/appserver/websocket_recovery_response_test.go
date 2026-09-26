package appserver

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestResponseWrittenClearsRecoveryOnlyForItsOwnResponse pins the fix for a
// production race: recoveryRunning used to clear in a defer once the
// force-stop handler goroutine returned, which races the same response's
// delivery to the client — the send loop must still dequeue and transmit the
// frame, and nothing ordered that against the goroutine's trivial deferred
// cleanup. Under scheduling contention a client that pipelined a second,
// fully sequential force stop right behind the first could arrive and be
// checked before the defer ran, and be refused with "force stop is already
// running" for a recovery that had already finished
// (TestDaemonActionRefusesStaleRenderedIdentity's flake).
//
// The fix moves the clear into responseWritten, which the send loop calls
// synchronously immediately after the transport accepts the exact frame the
// in-flight recovery is waiting on — so the flag cannot still read busy once
// that response is on its way out. This test drives responseWritten directly
// against a hand-set recovery state, the same way other Connection-internal
// tests in this package reach past the transport: it proves the clear is keyed
// to the specific response the recovery is waiting for, not to "a response was
// written."
func TestResponseWrittenClearsRecoveryOnlyForItsOwnResponse(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	conn := server.NewConnection("test-conn")

	recoveryRequestID := appwire.NewIntID(7)
	conn.mu.Lock()
	conn.recoveryRunning = true
	conn.recoveryResponseID = requestIDKey(recoveryRequestID)
	conn.mu.Unlock()

	// An unrelated response reaching the transport first must not clear a
	// different in-flight recovery's busy flag.
	conn.responseWritten(appwire.ResponseMessage(appwire.NewIntID(8), appwire.EmptyResponse{}))
	conn.mu.RLock()
	stillBusy := conn.recoveryRunning
	conn.mu.RUnlock()
	if !stillBusy {
		t.Fatal("an unrelated response written cleared a different in-flight recovery")
	}

	// The recovery's own response reaching the transport clears it.
	conn.responseWritten(appwire.ResponseMessage(recoveryRequestID, appwire.EmptyResponse{}))
	conn.mu.RLock()
	busy, responseID := conn.recoveryRunning, conn.recoveryResponseID
	conn.mu.RUnlock()
	if busy || responseID != "" {
		t.Fatalf("recovery still busy after its own response was written: busy=%v responseID=%q", busy, responseID)
	}
}

// TestResponseWrittenClearsRecoveryOnErrorResponse covers the handler-error
// and panic-recovered arms, which answer with appwire.ErrorMessage rather than
// appwire.ResponseMessage but carry the same request id and must clear the
// same way.
func TestResponseWrittenClearsRecoveryOnErrorResponse(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", Version: "test", SourceID: "local"})
	conn := server.NewConnection("test-conn")

	recoveryRequestID := appwire.NewIntID(9)
	conn.mu.Lock()
	conn.recoveryRunning = true
	conn.recoveryResponseID = requestIDKey(recoveryRequestID)
	conn.mu.Unlock()

	conn.responseWritten(appwire.ErrorMessage(recoveryRequestID, appwire.Unavailable("boom")))
	conn.mu.RLock()
	busy, responseID := conn.recoveryRunning, conn.recoveryResponseID
	conn.mu.RUnlock()
	if busy || responseID != "" {
		t.Fatalf("recovery still busy after its error response was written: busy=%v responseID=%q", busy, responseID)
	}
}
