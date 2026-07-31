package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// clearConcurrencyBudget bounds the parks below so a gate that never releases
// fails with its own message instead of hanging until the package timeout.
// Nothing here waits on wall-clock time in the passing path: every park is
// released by another goroutine reaching a named point.
const clearConcurrencyBudget = 30 * time.Second

// TestClearRefusesASecondConcurrentClear pins what the LOSER of two concurrent
// POST /clear requests observes: 409, and no second clear.
//
// The endpoint's only gate was `processing`, which is false for the whole of a
// clear -- so two POSTs arriving while the session is idle both ran the clear
// callback. In the daemon that callback builds a replacement session, publishes
// an identity for it and swaps it in (cmd/serf/serve.go's SetClearFunc), so two
// of them leave one replacement current and the other reachable by nothing.
// Nothing closes it, so its env's Cleanup() never runs and the scratch
// directory it owns outlives the daemon (kata x058 walked the same consequence
// from the other direction).
//
// Refusing is the answer rather than queueing the second behind the first:
// running both costs a whole session lifecycle -- SessionStart and SessionEnd
// hooks, a provisioned sandbox, a transcript -- for a thread every subscriber
// watches open and close in the same breath, and parking the second request
// inside the handler would block it on Session.Close()'s SessionEnd hooks,
// which run arbitrary user commands under a 10s timeout. 409 also stays inside
// the vocabulary this endpoint already speaks for "not right now", and it is
// the only answer that stays honest when the first clear FAILS: a coalesced
// second request would have to report an error for work it never attempted.
func TestClearRefusesASecondConcurrentClear(t *testing.T) {
	srv := NewServer(ServerConfig{})

	var mu sync.Mutex
	calls := 0
	insideFirst := make(chan struct{})
	release := make(chan struct{})
	srv.SetClearFunc(func(context.Context) error {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if !first {
			return nil
		}
		// Hold the first clear open across the second request. This is the
		// window a real clear spends building and swapping a session.
		close(insideFirst)
		select {
		case <-release:
			return nil
		case <-time.After(clearConcurrencyBudget):
			return errors.New("the first clear was never released")
		}
	})

	postClear := func() int {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/clear", nil))
		return rec.Code
	}

	firstCode := make(chan int, 1)
	go func() { firstCode <- postClear() }()

	select {
	case <-insideFirst:
	case <-time.After(clearConcurrencyBudget):
		t.Fatal("the first POST /clear never reached the clear callback, so the concurrent " +
			"window under test was never entered")
	}

	if code := postClear(); code != http.StatusConflict {
		t.Errorf("second concurrent POST /clear = %d, want %d: it ran a second clear while the "+
			"first was still in flight, and in the daemon that leaves one of the two replacement "+
			"sessions installed nowhere and closed by nobody", code, http.StatusConflict)
	}

	close(release)
	select {
	case code := <-firstCode:
		if code != http.StatusNoContent {
			t.Errorf("first POST /clear = %d, want %d", code, http.StatusNoContent)
		}
	case <-time.After(clearConcurrencyBudget):
		t.Fatal("the first POST /clear never returned after it was released")
	}

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("clear callback ran %d time(s) for two concurrent POST /clear, want 1", got)
	}

	// The gate is a window, not a latch: once the first clear has returned, the
	// endpoint clears again.
	if code := postClear(); code != http.StatusNoContent {
		t.Errorf("POST /clear after the first one finished = %d, want %d: the in-flight gate "+
			"never released, so this daemon can never clear again", code, http.StatusNoContent)
	}
	mu.Lock()
	got = calls
	mu.Unlock()
	if got != 2 {
		t.Errorf("clear callback ran %d time(s) in total, want 2 (one refused, two run)", got)
	}
}

// TestClearReleasesItsGateWhenTheClearFails covers the half of the gate a
// success path cannot: a clear that returns an error still has to hand the
// endpoint back. Every fallible step in the daemon's clear -- projecting the
// replacement's transcript, updating the rendezvous -- returns an error with
// the old session still live and still clearable, so a gate released only on
// success turns one transient failure into a /clear that is refused forever.
func TestClearReleasesItsGateWhenTheClearFails(t *testing.T) {
	srv := NewServer(ServerConfig{})

	calls := 0
	failure := errors.New("clear probe failure")
	srv.SetClearFunc(func(context.Context) error {
		calls++
		if calls == 1 {
			return failure
		}
		return nil
	})

	postClear := func() int {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/clear", nil))
		return rec.Code
	}

	if code := postClear(); code != http.StatusInternalServerError {
		t.Fatalf("failing POST /clear = %d, want %d", code, http.StatusInternalServerError)
	}
	if code := postClear(); code != http.StatusNoContent {
		t.Errorf("POST /clear after a failed one = %d, want %d: the failed clear kept the "+
			"in-flight gate, so the endpoint is refused forever after one recoverable failure",
			code, http.StatusNoContent)
	}
	if calls != 2 {
		t.Errorf("clear callback ran %d time(s), want 2", calls)
	}
}
