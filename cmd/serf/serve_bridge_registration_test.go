package main

import (
	"context"
	"net"
	"net/http"
	"sync"
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

// TestServeStartsNoBridgeOnceTeardownHasSnapshotItsDrains pins the other half of
// the same invariant: not only is every entry in bridgeDrains a real drain,
// every real drain is an entry.
//
// Holding drainsMu across deps.bridge and the append makes a half-registered
// session unobservable. It does nothing for a bridgeSession that begins
// ENTIRELY AFTER the teardown's snapshot: that drain is not in the snapshot at
// all, so the wait finishes on the drains it saw and closeEventObserver runs
// with a new drain still delivering. The next event that drain hands the
// observer is `t.ch <- ev` on a closed channel -- send on closed channel, on
// the drain's own goroutine, where nothing recovers it. Third instance of that
// crash on this branch, and newly possible: before it there was no tee to
// close.
//
// Reachable, not theoretical. POST /clear is a live route, and the shutdown
// goroutine's httpSrv.Close() joins no handlers -- it waits on the listener
// group, then force-closes connections and returns. The clear closure never
// consults the cancelled request context, so it runs to completion regardless,
// and it builds a whole replacement session on the way.
//
// A session arriving that late gets no bridge, and that is the right answer
// rather than the lesser evil. By then the listener is closed, the rendezvous
// entry is gone (an inner defer removed it), and the input loop has exited: the
// replacement can never take a turn and no client can ever read it. What an
// unbridged session normally costs -- a projection permanently missing events
// for the life of an identity -- needs a reader that outlives the event, and
// this process is exiting. Bridging it instead would register a LOSSLESS
// consumer, whose feed blocks its emitter, on a session nothing will ever
// close.
//
// The /clear runs from inside drainWaitExpiry, which serve calls after the
// snapshot and before the close. The window is entered by construction; no
// clock and no second goroutine are involved.
func TestServeStartsNoBridgeOnceTeardownHasSnapshotItsDrains(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	args = append(args, "--verbose")
	deps.verboseOut = newDiscardWriter()

	var mu sync.Mutex
	var teardownBegun bool
	var lateObservers []func(events.SessionEvent)
	deps.bridge = func(_ serveServer, _ *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		mu.Lock()
		late := teardownBegun
		if late {
			lateObservers = append(lateObservers, observer)
		}
		mu.Unlock()
		if late {
			// Report nothing. A drain still delivering when the tee closes is
			// the state under test; reporting completion would hide it.
			return
		}
		onDrained()
	}

	realExpiry := deps.drainWaitExpiry
	deps.drainWaitExpiry = func() <-chan time.Time {
		// The snapshot has been taken and the tee is still open: exactly where
		// a /clear handler that outlived httpSrv.Close() lands.
		mu.Lock()
		teardownBegun = true
		mu.Unlock()
		if err := state.srv.clear(context.Background()); err != nil {
			t.Errorf("clear during teardown: %v", err)
		}
		return realExpiry()
	}

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
		t.Fatal("serve never returned")
	}

	replacement := state.session(1)
	t.Cleanup(func() {
		// serve closed the session that was current when shutdown began, which
		// was not this one. Nothing else will.
		if replacement != nil {
			replacement.Close()
		}
	})
	if replacement == nil {
		t.Fatal("the /clear driven inside the teardown window built no replacement session, " +
			"so it never reached bridgeSession and this test proves nothing")
	}

	mu.Lock()
	late := make([]func(events.SessionEvent), len(lateObservers))
	copy(late, lateObservers)
	mu.Unlock()
	if len(late) == 0 {
		return
	}
	teeClosed := func() (panicked bool) {
		defer func() { panicked = recover() != nil }()
		late[0](events.SessionEvent{Kind: events.EventWarning, SessionID: "late"})
		return false
	}()
	t.Fatalf("serve started %d bridge(s) after teardown snapshotted its drains, so the wait "+
		"cannot see them; the verbose tee was closed under the late drain: %v. Its next event is "+
		"send on closed channel, on the drain's own goroutine, which takes the process down and "+
		"skips every defer above the teardown", len(late), teeClosed)
}
