package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/server"
)

// concurrentClearBudget bounds each park below. Nothing in the passing path
// waits on wall-clock time: every park is released by another goroutine
// reaching a named point, and the budget exists so a failure reports itself
// instead of hanging until the package timeout.
const concurrentClearBudget = 30 * time.Second

// TestServeConcurrentClearBuildsOneReplacement drives two overlapping POST
// /clear requests at a real serve run and requires exactly one replacement
// session to come out of them, with every session the daemon built closed by
// the time serve returns.
//
// The clear callback (SetClearFunc in serve.go) builds a fresh session,
// publishes an identity for it, updates the rendezvous and swaps it in. Two of
// them running at once each read the SAME live session as the one they replace
// and each install their own: whichever swap lands second is current, and the
// other replacement is reachable from nothing. Nothing closes it, so its env's
// Cleanup() never runs and the scratch directory it owns survives the daemon --
// the same disk-state consequence kata x058 fixed for a replacement installed
// past shutdown's closing pass, reached here on a plainly running daemon.
//
// The window is entered by construction rather than by timing. The first clear
// is parked inside prepareAppIdentity -- past the gate, before anything shared
// moves -- until the second request has been answered, so "concurrent" is a
// fact about the program's position and not about the scheduler.
//
// Expiry is a channel that never fires, so what this measures is the closes.
// Every session the daemon makes current has a closer (shutdown's pass, or the
// /clear that installed it past that pass), and a bridge drain ends only after
// its session's channel is closed, which happens strictly after env.Cleanup().
// A replacement nothing closes therefore parks the teardown forever, which is
// what the budgeted wait for serve to return reports.
func TestServeConcurrentClearBuildsOneReplacement(t *testing.T) {
	deps, state, args := newClearServeDeps(t)

	never := make(chan time.Time)
	deps.drainWaitExpiry = func() <-chan time.Time { return never }

	// Park the FIRST clear-time identity preparation. The daemon prepares once
	// at startup, so call 2 is the first clear's; later calls run through, which
	// is what lets a second clear complete -- and be seen completing -- when the
	// gate under test is missing.
	prepare := deps.prepareAppIdentity
	var mu sync.Mutex
	prepares := 0
	insideFirstClear := make(chan struct{})
	release := make(chan struct{})
	deps.prepareAppIdentity = func(sourceID, threadID, transcriptPath string) (server.PreparedAppIdentity, error) {
		mu.Lock()
		prepares++
		firstClear := prepares == 2
		mu.Unlock()
		if firstClear {
			close(insideFirstClear)
			select {
			case <-release:
			case <-time.After(concurrentClearBudget):
				return server.PreparedAppIdentity{}, errors.New("the first clear was never released")
			}
		}
		return prepare(sourceID, threadID, transcriptPath)
	}

	// Sampled on the serveHTTP goroutine only, and read after it has finished.
	var firstCode, secondCode int
	sampled := make(chan struct{})
	deps.serveHTTP = func(_ *http.Server, ln net.Listener) error {
		defer close(sampled)
		// The real route, guards and all: the daemon's same-origin policy
		// allows only its own listener address as the request Host.
		postClear := func() int {
			req := httptest.NewRequest(http.MethodPost, "/clear", nil)
			req.Host = ln.Addr().String()
			rec := httptest.NewRecorder()
			state.srv.ServeHTTP(rec, req)
			return rec.Code
		}

		first := make(chan int, 1)
		go func() { first <- postClear() }()

		select {
		case <-insideFirstClear:
		case <-time.After(concurrentClearBudget):
			t.Error("the first POST /clear never reached identity preparation, so the " +
				"concurrent window under test was never entered")
			return http.ErrServerClosed
		}

		secondCode = postClear()
		close(release)

		select {
		case firstCode = <-first:
		case <-time.After(concurrentClearBudget):
			t.Error("the first POST /clear never returned after it was released")
			return http.ErrServerClosed
		}

		state.srv.shutdown()
		return http.ErrServerClosed
	}

	served := make(chan error, 1)
	go func() { served <- runServeWithDeps(args, deps) }()

	select {
	case <-sampled:
	case <-time.After(concurrentClearBudget):
		t.Fatal("the serve run never reached its /clear requests")
	}

	if secondCode != http.StatusConflict {
		t.Errorf("second concurrent POST /clear = %d, want %d", secondCode, http.StatusConflict)
	}
	if firstCode != http.StatusNoContent {
		t.Errorf("first POST /clear = %d, want %d", firstCode, http.StatusNoContent)
	}
	if extra := state.session(2); extra != nil {
		t.Errorf("two concurrent /clear built a THIRD session %s; only one of the two "+
			"replacements can be current and nothing closes the other", extra.ID())
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(concurrentClearBudget):
		t.Fatal("serve never returned: a replacement session that nothing closes keeps the " +
			"bridge drain registered for it from ever ending, so the teardown is parked on it. " +
			"In production that spends the whole shutdown budget and then abandons the verbose tee")
	}

	for i := 0; ; i++ {
		sess := state.session(i)
		if sess == nil {
			if i < 2 {
				t.Fatalf("the daemon built %d session(s); want the original and one replacement", i)
			}
			break
		}
		if got := sess.State(); got != agent.SessionClosed {
			t.Errorf("session %d (%s) is %q after serve returned, want %q: nothing closes it, so "+
				"its env's Cleanup() never runs and the scratch directory it owns outlives the daemon",
				i, sess.ID(), got, agent.SessionClosed)
		}
	}
}
