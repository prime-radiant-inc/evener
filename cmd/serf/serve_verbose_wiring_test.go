package main

import (
	"net"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
)

// TestServeInstallsANonBlockingVerboseObserver pins the --verbose WIRING, not
// the tee type.
//
// The tee is pinned on its own, but reverting serve.go to the observer it
// replaced -- an encoder guarded by a mutex, called synchronously -- leaves that
// pin green, because nothing else asserts which observer the daemon actually
// installs. R-1 exists to keep stderr off the bridge goroutine, so the wiring is
// the thing worth pinning.
//
// It captures the observer serve.go hands the bridge, points --verbose at a
// writer that never returns, and asserts the observer stays live past the
// buffer. A synchronous encoder blocks on the first call.
func TestServeInstallsANonBlockingVerboseObserver(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	args = append(args, "--verbose")

	stalled := newBlockingWriter()
	deps.verboseOut = stalled

	observed := make(chan func(events.SessionEvent), 1)
	deps.bridge = func(_ serveServer, _ *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		select {
		case observed <- observer:
		default:
		}
		onDrained()
	}

	var blocked bool
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		var observer func(events.SessionEvent)
		select {
		case observer = <-observed:
		case <-time.After(10 * time.Second):
			state.srv.shutdown()
			return http.ErrServerClosed
		}
		if observer == nil {
			state.srv.shutdown()
			return http.ErrServerClosed
		}
		// Far more than the tee's buffer, against a writer that never returns.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range verboseEventTeeBuffer * 2 {
				observer(events.SessionEvent{Kind: events.EventWarning, SessionID: "s1"})
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			blocked = true
		}
		// Released HERE, inside the serve run, not in a defer around it. serve's
		// own teardown closes the tee and waits for its writer goroutine, which
		// is parked on this very write — so releasing after serve returns would
		// deadlock the thing under test rather than measure it.
		stalled.unblock()
		state.srv.shutdown()
		return http.ErrServerClosed
	}

	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if blocked {
		t.Fatal("the observer serve installed BLOCKS on a stalled stderr: " +
			"the daemon's authoritative consumer is coupled to whatever reads its logs")
	}
}
