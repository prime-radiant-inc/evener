package agent

import (
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// newDisposeCloseTestSession builds a minimal session for the dispose-vs-close
// protocol tests (spec §P1 test 15). It runs no turn; only the lifecycle
// concurrency primitives are exercised.
func newDisposeCloseTestSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// TestSession_Close_JoinsInFlightDispose is spec test 15(a): a dispose op
// admitted before close (holding disposeWG, releasing on ctx cancel) must block
// Close()'s drain until it releases. Close cancels the turn ctx, then joins,
// then drains — proven by observing that Close() has NOT returned at the moment
// the holder releases.
func TestSession_Close_JoinsInFlightDispose(t *testing.T) {
	t.Parallel()
	sess := newDisposeCloseTestSession(t)

	started := make(chan struct{})
	releasing := make(chan struct{})
	admitted := make(chan bool, 1)
	go func() {
		ok := sess.beginDispose()
		admitted <- ok
		close(started)
		if !ok {
			return
		}
		// Simulate a mid-dispose git op that is interruptible: block until
		// close cancels the turn ctx (step 2 of the protocol).
		<-sess.sessionCtx.Done()
		// Signal we observed the cancel and are about to release. This is
		// strictly ordered BEFORE endDispose(), so if Close()'s join respects
		// the WaitGroup, Close cannot have progressed past the join yet.
		close(releasing)
		sess.endDispose()
	}()
	<-started
	if !<-admitted {
		t.Fatal("beginDispose refused admission before closing")
	}

	closeDone := make(chan struct{})
	go func() {
		sess.Close()
		close(closeDone)
	}()

	// Close must block on the join until the holder releases: at the instant
	// the holder signals `releasing` (before its endDispose), Close must not
	// yet have returned.
	select {
	case <-closeDone:
		t.Fatal("Close returned before the in-flight dispose released the WaitGroup")
	case <-releasing:
		// Correct: close cancelled the ctx, the holder observed it and is
		// releasing, and Close is still parked in disposeWG.Wait().
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the dispose holder to observe ctx cancel")
	}

	select {
	case <-closeDone:
		// Correct: once the WaitGroup drained, Close proceeded to drain/teardown.
	case <-time.After(5 * time.Second):
		t.Fatal("Close deadlocked after the in-flight dispose released")
	}
}

// TestSession_beginDispose_RefusedAfterClosing is spec test 15(b): once close
// has set `closing`, no new dispose op may be admitted.
func TestSession_beginDispose_RefusedAfterClosing(t *testing.T) {
	t.Parallel()
	sess := newDisposeCloseTestSession(t)
	sess.Close()
	if sess.beginDispose() {
		t.Fatal("beginDispose admitted a dispose op after the session closed")
	}
}

// TestSession_Close_ConcurrentCallsSafe is spec test 15(d): overlapping Close()
// calls remain safe (closeOnce) even with the restructured set-flag → cancel →
// join → drain preamble.
func TestSession_Close_ConcurrentCallsSafe(t *testing.T) {
	t.Parallel()
	sess := newDisposeCloseTestSession(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess.Close()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent Close() calls deadlocked")
	}
}
