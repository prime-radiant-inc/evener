package agent

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestSession_ConcurrencyStress is Phase 10 W2: a TRUE-concurrency stress of the
// session. The Phase 8/9 stateful models are single-goroutine-deterministic by
// design; this drives the session's GENUINELY concurrent surface in parallel —
// a serial turn loop (the supported one-turn-at-a-time path) raced against a
// concurrent interrupter (ctx cancel, the real interrupt path) and the broad set
// of s.mu-guarded control/read ops (SetModel / SetReasoningEffort / DetailedStatus
// / SetGoal / ClearGoal / State / Meta). It extends the focused
// TestSession_*_NoRaceWithSetters race tests to the full op mix at stress scale.
//
// Oracles (the concurrency-appropriate set): the race detector (run under -race in
// the nightly — the headline) finds data races the deterministic models can't;
// the deadline watchdog turns a deadlock into a loud panic with the stuck
// goroutines; and a clean Close afterwards proves no wedge / no panic. (Goroutine-
// leak detection — go.uber.org/goleak — is the intended addition once that dep is
// vendored; it is not available offline yet.)
//
// Gated behind -short: it is a nightly/-race stress run, not a fast-gate test, and
// being inherently nondeterministic it must never gate a PR.
func TestSession_ConcurrencyStress(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrency stress is a nightly/-race run; skipped under -short")
	}
	t.Parallel()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"}) // Stream unsupported -> Complete path
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Deadlock watchdog: a genuine wedge dumps every goroutine's stack and fails.
	watchdog := time.AfterFunc(60*time.Second, func() {
		panic("concurrency stress wedged: no progress in 60s (deadlock)")
	})
	defer watchdog.Stop()

	const iters = 200
	stop := make(chan struct{})
	var loops, bounded sync.WaitGroup

	// Serial turn loop (one turn at a time, the supported contract), each round on
	// a cancellable ctx published so the interrupter can cancel the in-flight turn.
	var cur struct {
		sync.Mutex
		cancel context.CancelFunc
	}
	loops.Add(1)
	go func() {
		defer loops.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ctx, cancel := context.WithCancel(context.Background())
			cur.Lock()
			cur.cancel = cancel
			cur.Unlock()
			_, _ = sess.ProcessInput(ctx, "hi", nil)
			cancel()
		}
	}()

	// Concurrent interrupter: cancels whatever turn is in flight (real interrupt).
	loops.Add(1)
	go func() {
		defer loops.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			cur.Lock()
			fn := cur.cancel
			cur.Unlock()
			if fn != nil {
				fn()
			}
			runtime.Gosched()
		}
	}()

	// Bounded concurrent control/read ops — all s.mu-guarded, safe against a turn.
	ops := []func(){
		func() { sess.SetModel("gpt-5.2") },
		func() { sess.SetReasoningEffort("high") },
		func() { _ = sess.DetailedStatus() },
		func() { _, _ = sess.SetGoal(context.Background(), "do the thing") },
		func() { sess.ClearGoal() },
		func() { _ = sess.State() },
		func() { _ = sess.Meta() },
	}
	for _, fn := range ops {
		bounded.Add(1)
		go func(fn func()) {
			defer bounded.Done()
			for i := 0; i < iters; i++ {
				fn()
				runtime.Gosched()
			}
		}(fn)
	}

	bounded.Wait()
	close(stop)
	loops.Wait()

	// Consistency: the session settles to a valid boundary state and Close is clean
	// (the deferred Close + the watchdog catch a wedge; -race catches a data race).
	if st := sess.State(); st != SessionIdle && st != SessionClosed {
		t.Fatalf("post-stress state = %q, want idle or closed", st)
	}
}
