package main

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
)

// TestServeRegistersNoDrainItsBridgeNeverStarted pins the invariant behind
// bridgeDrains: an entry there means a drain EXISTS that will close that
// channel. Registering before deps.bridge has been called breaks it.
//
// The consequence is not a crash but silence. bridgeSession also runs from
// SetClearFunc on a net/http handler goroutine, and net/http recovers handler
// panics, so a bridge that panicked would leave the daemon alive with a phantom
// in the list -- and shutdown would then burn its whole drain budget on a
// channel nothing will ever close, abandoning the verbose tee at the end of it.
// A loud panic becomes a quiet truncation.
//
// Neither ConsumeEventsLossless panic condition can fire from serve.go today.
// This is a trap for the next caller, which is exactly the kind this branch has
// spent its life on, and it costs one lock to make unspellable.
//
// The probe uses the STARTUP call site rather than /clear because it is the same
// closure and needs no recovering handler to observe the difference: serve's
// teardown defer runs while the panic unwinds, so a phantom registration parks
// it and the panic never surfaces at all.
//
// Expiry is injected as a channel that never fires, so this measures the
// registration and nothing else -- with the production budget a phantom would
// eventually time out, and the test would be reading the budget rather than the
// invariant it is here for.
func TestServeRegistersNoDrainItsBridgeNeverStarted(t *testing.T) {
	deps, _, args := newClearServeDeps(t)
	deps.bridge = func(serveServer, *agent.Session, func(events.SessionEvent), func()) {
		panic("bridge failed before it started a drain")
	}
	never := make(chan time.Time)
	deps.drainWaitExpiry = func() <-chan time.Time { return never }

	returned := make(chan any, 1)
	go func() {
		defer func() { returned <- recover() }()
		_ = runServeWithDeps(args, deps)
	}()

	select {
	case r := <-returned:
		if r == nil {
			t.Fatal("the bridge panic did not reach the caller; serve swallowed it")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve never returned: its teardown is waiting on a drain the bridge never " +
			"started, so on the /clear path -- where net/http recovers the panic -- the daemon " +
			"would stall its whole shutdown on a channel nothing can close")
	}
}
