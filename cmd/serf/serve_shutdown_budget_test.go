package main

import (
	"net"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
)

// TestServeWaitsForALiveDrainWithinTheBudget pins the arm the budget must not
// touch: a drain that is merely still working is waited for, in full.
//
// The budget exists for a wedged drain. If it ever expires on a healthy one it
// stops being a backstop and becomes a silent truncation of the very tail the
// wait was added to deliver -- and worse, it would mask a real slowness bug
// behind a shutdown that looks clean. This is the pin that says so: the drain
// finishes only when the test lets it, and serve must not move until then.
//
// The budget it runs against is the PRODUCTION one. drainWaitExpiry is wrapped
// rather than replaced, so shrinking shutdownDrainWaitBudget still reaches the
// timer under test; the wrapper only reports when the wait began, which is what
// makes the assertion below tight instead of a guess about serve's startup
// cost.
func TestServeWaitsForALiveDrainWithinTheBudget(t *testing.T) {
	deps, state, args := newClearServeDeps(t)

	release := make(chan struct{})
	deps.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
		go func() {
			<-release
			onDrained()
		}()
	}

	realExpiry := deps.drainWaitExpiry
	waitStarted := make(chan struct{})
	deps.drainWaitExpiry = func() <-chan time.Time {
		close(waitStarted)
		return realExpiry()
	}

	deps.serveHTTP = func(*http.Server, net.Listener) error {
		state.srv.shutdown()
		return http.ErrServerClosed
	}

	served := make(chan error, 1)
	go func() { served <- runServeWithDeps(args, deps) }()

	select {
	case <-waitStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("serve never reached its drain wait")
	}

	// A grace, not a race. Under the production budget serve cannot leave the
	// select at all until the drain reports; under a budget short enough to
	// absorb real work it leaves within microseconds of the wait starting, so
	// any window that survives goroutine scheduling separates the two.
	select {
	case err := <-served:
		t.Fatalf("serve abandoned a live drain instead of waiting for it (returned %v): "+
			"the shutdown budget is absorbing normal work, so a slow drain now truncates "+
			"the tail silently instead of being delivered", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve never returned after its drain reported completion")
	}
}

// TestServeAbandonsAWedgedDrainWithoutClosingTheObserver pins the other arm,
// and specifically pins what expiry MUST NOT do.
//
// A drain wedged inside BridgeEvent used to make the daemon ignore SIGTERM
// until a supervisor SIGKILLed it. The budget ends that. But the obvious
// implementation of a budget -- expire, then close the tee and carry on -- puts
// back the crash the wait was added to prevent: observe on a closed tee is
// `t.ch <- ev` on a closed channel, which panics on the DRAIN's goroutine, takes
// the process down and skips every defer registered above the teardown. Expiry
// must therefore abandon the tee, not close it. A truncated diagnostic tail on a
// process that is already exiting is the whole cost.
//
// The probe is the real mechanism rather than a proxy: the observer serve built
// is captured through the bridge and called after serve returns. On an abandoned
// tee that is an ordinary queued write; on a closed one it is the panic.
//
// Expiry is driven by an already-closed channel, so the arm under test is
// selected by construction and no clock is involved.
func TestServeAbandonsAWedgedDrainWithoutClosingTheObserver(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	args = append(args, "--verbose")
	deps.verboseOut = newDiscardWriter()

	// A drain that never reports: onDrained is dropped on the floor, which is
	// what a bridge wedged inside BridgeEvent looks like from here.
	observed := make(chan func(events.SessionEvent), 1)
	deps.bridge = func(_ serveServer, _ *agent.Session, observer func(events.SessionEvent), _ func()) {
		select {
		case observed <- observer:
		default:
		}
	}

	expired := make(chan time.Time)
	close(expired)
	deps.drainWaitExpiry = func() <-chan time.Time { return expired }

	deps.serveHTTP = func(*http.Server, net.Listener) error {
		state.srv.shutdown()
		return http.ErrServerClosed
	}

	served := make(chan error, 1)
	go func() { served <- runServeWithDeps(args, deps) }()

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve never returned with its drain wedged: the shutdown budget did not expire, " +
			"so the daemon still ignores SIGTERM until something kills it")
	}

	var observer func(events.SessionEvent)
	select {
	case observer = <-observed:
	default:
		t.Fatal("serve never handed its observer to the bridge; this test would prove nothing")
	}

	closed := func() (panicked bool) {
		defer func() { panicked = recover() != nil }()
		observer(events.SessionEvent{Kind: events.EventWarning, SessionID: "wedged"})
		return false
	}()
	if closed {
		t.Fatal("expiry CLOSED the verbose sink with a drain still live: the next event the drain " +
			"delivers panics with send on closed channel, on the drain's own goroutine, which is " +
			"exactly the shutdown crash the wait exists to prevent")
	}
}
