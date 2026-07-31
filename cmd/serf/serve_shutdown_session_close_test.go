package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
)

// TestServeClosesAReplacementInstalledDuringShutdown pins the invariant the
// shutdown drain wait rests on: the daemon closes EVERY session it makes
// current, not only the one that happened to be live when shutdown ran.
//
// Shutdown makes ONE closing pass (closeLiveSession) once the input loop has
// exited. POST /clear is still a live route at that moment -- httpSrv.Close()
// joins no handlers, and the clear closure never consults the cancelled request
// context -- so a /clear can install a whole replacement session just after
// that pass, and the pass never comes round again. Left to shutdown alone the
// replacement is simply never closed, and everything Session.Close() does never
// happens for it:
//
//   - its event channel is never closed, so the bridge drain registered for it
//     can never end. The teardown wait then spends its ENTIRE budget on a drain
//     that is neither wedged nor working and abandons the verbose tee at the end
//     of it -- the case the budget's rationale used to say could not occur.
//   - its env's Cleanup() never runs, so the scratch directory the session owns
//     survives process exit. Same missed close, second consequence.
//
// Serve returning here covers both, and that is not a shortcut. Session.close()
// runs env.Cleanup() (its step 4) strictly before close(s.events) at the very
// end, so a drain that ENDED proves the channel was closed, which proves
// Cleanup had already run.
//
// The window is entered by construction; no clock and no second goroutine
// decide anything. serveHTTP cancels the context and then parks until the FIRST
// session's own drain reports, which can only happen after Close() closed its
// channel -- so shutdown's pass has demonstrably run, and it picked session 0.
// The /clear then runs from inside serveHTTP, which serve has not returned from
// yet, so the teardown snapshot has not been taken and teardownStarted -- which
// guards the adjacent, already-closed AFTER case -- is still false.
//
// Expiry is a channel that never fires, so this measures the close and nothing
// else: under the production budget an unclosed replacement would eventually
// time out, and the test would be reading the budget rather than the invariant
// it is here for.
func TestServeClosesAReplacementInstalledDuringShutdown(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	args = append(args, "--verbose")
	deps.verboseOut = newDiscardWriter()

	never := make(chan time.Time)
	deps.drainWaitExpiry = func() <-chan time.Time { return never }

	// Report when the FIRST session's drain ends. That is the signal that
	// shutdown's closing pass has run and chose session 0, which is what puts
	// the /clear below on the far side of it.
	realBridge := deps.bridge
	var mu sync.Mutex
	bridged := 0
	firstDrained := make(chan struct{})
	deps.bridge = func(s serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		mu.Lock()
		bridged++
		first := bridged == 1
		mu.Unlock()
		if !first {
			realBridge(s, sess, observer, onDrained)
			return
		}
		realBridge(s, sess, observer, func() {
			onDrained()
			close(firstDrained)
		})
	}

	cleared := make(chan error, 1)
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		state.srv.shutdown()
		select {
		case <-firstDrained:
		case <-time.After(30 * time.Second):
			cleared <- errors.New("the first session's drain never ended, so shutdown's closing " +
				"pass never ran and the window under test was never entered")
			return http.ErrServerClosed
		}
		cleared <- state.srv.clear(context.Background())
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
		t.Fatal("serve never returned: the /clear that landed after shutdown's closing pass left a " +
			"session nothing will close, so the bridge drain registered for it can never end and " +
			"the teardown is parked on it. In production that spends the whole shutdown budget and " +
			"then abandons the verbose tee")
	}

	if err := <-cleared; err != nil {
		t.Fatalf("clear during shutdown: %v", err)
	}

	replacement := state.session(1)
	if replacement == nil {
		t.Fatal("the /clear driven after shutdown's closing pass built no replacement session, " +
			"so it never reached the swap and this test proves nothing")
	}
	if got := replacement.State(); got != agent.SessionClosed {
		t.Fatalf("the replacement session installed after shutdown's closing pass is %q, want %q. "+
			"Nothing else closes it, so its env's Cleanup() never runs and the scratch directory "+
			"it owns outlives the process", got, agent.SessionClosed)
	}
}
