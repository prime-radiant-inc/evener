package agent

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// closeParkBudget bounds the wait for the closing goroutine to reach its first
// lock acquisition. It is a resource bound on a failing run: the goroutine is
// started and then does nothing but take that lock, so a healthy run converges
// in microseconds.
const closeParkBudget = 10 * time.Second

// TestCloseWaitsForResponseSideEffectsMuWithoutHoldingSessionMu pins the lock
// order Close takes its two flags under (session_lifecycle.go:
// responseSideEffectsMu, THEN mu), which is what keeps a wedged emitter from
// becoming a three-party cycle.
//
// The pieces, and why the order is load-bearing rather than stylistic:
//
//   - An emitter whose buffer is full waits inside responseSideEffectsMu — that
//     is the deliberate shape of the tool side-effect bundle plus a lossless feed
//     (session_events.go's sendEvent, session_tools.go's chunk loop).
//   - The daemon's authoritative consumer samples the session on its drain
//     goroutine, and Session.mu is the one lock that contract lets it take
//     (session_envelope_sampling.go).
//   - So anything that holds s.mu while WAITING on responseSideEffectsMu closes
//     the loop: the sampler cannot get s.mu, the drain stops, the buffer stays
//     full, and the emitter never releases responseSideEffectsMu. Close is the
//     one function in the package that takes both, so Close is where the loop
//     would be closed.
//
// With the order as written, Close waits holding NOTHING, s.mu stays available,
// the drain keeps moving, and the emitter finishes. The test asserts exactly
// that: while a stand-in emitter holds responseSideEffectsMu and Close is parked,
// a sample still returns.
func TestCloseWaitsForResponseSideEffectsMuWithoutHoldingSessionMu(t *testing.T) {
	sess := newTestSession(t)

	// Stand in for the wedged emitter. A real one is blocked on the channel send
	// inside this same critical section; what matters to Close is only that the
	// lock is held by someone who is not going to give it back yet.
	sess.responseSideEffectsMu.Lock()
	releaseEmitter := releaseOnce(sess.responseSideEffectsMu.Unlock)
	defer releaseEmitter()

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		sess.Close()
	}()

	if !awaitParkedOnAMutexIn(t, "agent.(*Session).close") {
		return
	}

	// Meta is the consumer's sample here because it takes s.mu and nothing else.
	// If Close were holding s.mu while it waits, this never returns.
	sampled := make(chan struct{}, 1)
	go func() {
		sess.Meta()
		sampled <- struct{}{}
	}()
	select {
	case <-sampled:
	case <-time.After(samplerBudget):
		t.Errorf("a sample blocked on Session.mu for %s while Close waited for "+
			"responseSideEffectsMu: Close is holding s.mu across that wait, which makes a wedged "+
			"emitter unrecoverable — the consumer that would drain it cannot sample, so it stops "+
			"draining and the emitter never gets its buffer back. Take responseSideEffectsMu "+
			"BEFORE mu in Session.close.\n\n%s", samplerBudget, goroutineDump())
		return
	}

	releaseEmitter()
	select {
	case <-closed:
	case <-time.After(closeParkBudget):
		t.Fatalf("Close did not return within %s of the emitter releasing responseSideEffectsMu", closeParkBudget)
	}
}

// releaseOnce wraps an unlock so the deferred safety release is a no-op after
// the test has already released.
func releaseOnce(unlock func()) func() {
	done := false
	return func() {
		if done {
			return
		}
		done = true
		unlock()
	}
}

// awaitParkedOnAMutexIn waits until a goroutine STARTED BY THIS TEST is blocked
// on a mutex inside the named function, and reports whether it got there.
//
// Matching on the test's own "created by" frame as well as the function is what
// keeps this from reading some other test's leaked goroutine and declaring
// victory before the interesting one has even started.
func awaitParkedOnAMutexIn(t *testing.T, function string) bool {
	t.Helper()
	createdBy := "created by primeradiant.com/serf/agent." + t.Name()
	deadline := time.Now().Add(closeParkBudget)
	for {
		for g := range strings.SplitSeq(goroutineDump(), "\n\ngoroutine ") {
			if strings.Contains(g, function) &&
				strings.Contains(g, "sync.(*Mutex).Lock") &&
				strings.Contains(g, createdBy) {
				return true
			}
		}
		if time.Now().After(deadline) {
			t.Errorf("no goroutine this test started reached a mutex inside %s within %s; "+
				"the ordering this test exists to pin was never exercised.\n\n%s",
				function, closeParkBudget, goroutineDump())
			return false
		}
		runtime.Gosched()
	}
}
