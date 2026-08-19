package agent

import (
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
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

// closeJoinArrivalWindow bounds how long a broken Close may take to walk past an
// in-flight dispose/sweep and reach the post-join seam. With either Wait()
// deleted, Close reaches the seam in microseconds on this minimal session (no
// delegates, no subagents, no env teardown before the seam), so this leaves
// well over two orders of magnitude of margin against goroutine scheduling
// jitter. It is dead time only on the green path: a correct Close parks at the
// join and never reaches the seam while the work is held, so phase 1 always
// waits the full window before phase 2 releases the work. The bound is a
// tripwire only — the synchronization mechanism is the seam channel, not time
// (kata 0t1y: "Close has not returned yet" is unfalsifiable when Close blocks
// on a join, so the join is observed from inside the closing goroutine at the
// point immediately after both Wait() calls return).
const closeJoinArrivalWindow = time.Second

// closeJoinHangGuard is the generous tripwire ceiling on the phases that await
// a real completion signal (the seam channel, the dispose/sweep release, or
// Close's return). Every step here is in-process with no I/O, so these only
// fire on a genuine hang, never on scheduler contention under a loaded suite.
// It is a tripwire only — the synchronization mechanism is the seam channel,
// not time (docs/testing.md Flakes and Timeouts; kata 0t1y).
const closeJoinHangGuard = 30 * time.Second

// TestSession_Close_JoinsInFlightDispose is spec test 15(a): an in-flight
// dispose op admitted before Close (holding disposeWG) must block Close()'s
// drain until it releases. Close cancels the turn ctx, then joins, then drains.
//
// The proof is a POSITIVE observation taken on the closing goroutine at the
// closeAfterDisposeSweepJoin seam — the point in Close() immediately AFTER
// disposeWG.Wait() and sweepWG.Wait() have returned — not by watching Close
// fail to return. "Close has not returned yet" is unfalsifiable here (kata
// 0t1y): Close's post-join teardown (tree stop, drain, env cleanup) takes
// nonzero time, so a test that only checks Close has not returned at the
// instant the dispose holder observes the ctx cancel stays green with the join
// deleted — Close simply proceeds through the missing Wait and returns a few
// microseconds later. Observing Close at the post-join seam, with the dispose
// demonstrably still in flight, turns the red/green question into a positive
// fact: a Close that reached the seam before the dispose released walked past
// the join.
func TestSession_Close_JoinsInFlightDispose(t *testing.T) {
	t.Parallel()
	sess := newDisposeCloseTestSession(t)

	releaseDispose := make(chan struct{})
	disposeReleased := make(chan struct{})
	// seamArrived is buffered so the seam never blocks Close; seamRelease is
	// what holds Close at the seam long enough for the test to assert ordering.
	seamArrived := make(chan struct{}, 1)
	seamRelease := make(chan struct{})
	sess.cfg.testOnly.closeAfterDisposeSweepJoin = func() {
		select {
		case seamArrived <- struct{}{}:
		default:
		}
		<-seamRelease
	}

	// Admit an in-flight dispose op and hold it on releaseDispose so it cannot
	// release disposeWG until the test decides. beginDispose's Add happens
	// before Close()'s Wait (the goroutine starts after the admission).
	started := make(chan struct{})
	admitted := make(chan bool, 1)
	go func() {
		ok := sess.beginDispose()
		admitted <- ok
		close(started)
		if !ok {
			return
		}
		<-releaseDispose
		sess.endDispose()
		close(disposeReleased)
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

	// Phase 1 (red detection): if disposeWG.Wait() were deleted, Close would
	// proceed to the seam while the dispose is still held. A seam arrival here
	// is the failure; the bound is a tripwire only.
	select {
	case <-seamArrived:
		close(seamRelease)
		<-closeDone
		t.Fatal("Close passed the dispose/sweep join while an in-flight dispose was still held (disposeWG.Wait() missing or ineffective)")
	case <-time.After(closeJoinArrivalWindow):
		// Correct: Close is parked at disposeWG.Wait() and has not reached the seam.
	}

	// Phase 2 (positive confirmation): release the dispose. Close's join
	// returns and Close reaches the seam, where the dispose is now
	// demonstrably done.
	close(releaseDispose)
	select {
	case <-seamArrived:
		// Correct: Close reached the post-join seam after the dispose released.
	case <-time.After(closeJoinHangGuard):
		close(seamRelease)
		t.Fatal("Close never reached the post-dispose/sweep join after the in-flight dispose released")
	}
	// The dispose released strictly before Close reached the seam (it is what
	// unblocked the join), so confirm that ordering.
	select {
	case <-disposeReleased:
	case <-time.After(closeJoinHangGuard):
		close(seamRelease)
		t.Fatal("in-flight dispose never released its WaitGroup admission")
	}
	close(seamRelease)

	select {
	case <-closeDone:
		// Correct: once the join drained, Close proceeded through teardown.
	case <-time.After(closeJoinHangGuard):
		t.Fatal("Close did not return after the in-flight dispose released and the join completed")
	}
}

// TestSession_Close_JoinsInFlightSweep is spec test 15(a) for the sweep join:
// an in-flight P3 open-pass residue sweep (holding sweepWG) must block
// Close()'s drain until it releases. Same 0t1y positive-observation pattern
// as the dispose test, observing at the post-join seam so deleting
// sweepWG.Wait() reliably turns this red. The sweep is registered directly on
// sweepWG (mirroring fireOpenLaneResidueSweep's Add idiom) so the Add happens
// before Close()'s Wait without the full timer/local-env machinery; only the
// join's behavior is under test, not the sweep's git work.
func TestSession_Close_JoinsInFlightSweep(t *testing.T) {
	t.Parallel()
	sess := newDisposeCloseTestSession(t)

	releaseSweep := make(chan struct{})
	sweepReleased := make(chan struct{})
	seamArrived := make(chan struct{}, 1)
	seamRelease := make(chan struct{})
	sess.cfg.testOnly.closeAfterDisposeSweepJoin = func() {
		select {
		case seamArrived <- struct{}{}:
		default:
		}
		<-seamRelease
	}

	// Register an in-flight sweep on sweepWG and hold it on releaseSweep. The
	// Add happens before Close()'s Wait because Close starts after this.
	sess.sweepWG.Add(1)
	go func() {
		<-releaseSweep
		sess.sweepWG.Done()
		close(sweepReleased)
	}()

	closeDone := make(chan struct{})
	go func() {
		sess.Close()
		close(closeDone)
	}()

	// Phase 1 (red detection): if sweepWG.Wait() were deleted, Close would
	// proceed to the seam while the sweep is still held.
	select {
	case <-seamArrived:
		close(seamRelease)
		<-closeDone
		t.Fatal("Close passed the dispose/sweep join while an in-flight sweep was still held (sweepWG.Wait() missing or ineffective)")
	case <-time.After(closeJoinArrivalWindow):
		// Correct: Close is parked at sweepWG.Wait() and has not reached the seam.
	}

	// Phase 2 (positive confirmation): release the sweep; Close reaches the seam.
	close(releaseSweep)
	select {
	case <-seamArrived:
		// Correct: Close reached the post-join seam after the sweep released.
	case <-time.After(closeJoinHangGuard):
		close(seamRelease)
		t.Fatal("Close never reached the post-dispose/sweep join after the in-flight sweep released")
	}
	select {
	case <-sweepReleased:
	case <-time.After(closeJoinHangGuard):
		close(seamRelease)
		t.Fatal("in-flight sweep never released its WaitGroup admission")
	}
	close(seamRelease)

	select {
	case <-closeDone:
	case <-time.After(closeJoinHangGuard):
		t.Fatal("Close did not return after the in-flight sweep released and the join completed")
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
	for range 8 {
		wg.Go(func() {
			sess.Close()
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		// TRIPWIRE: 8 concurrent Close() calls against closeOnce settle
		// in-process with no real I/O; only fires on a genuine hang.
	case <-time.After(closeJoinHangGuard):
		t.Fatal("concurrent Close() calls deadlocked")
	}
}
