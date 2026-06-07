package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/llm"
)

// TestGoal_NoRaceSetClearVsGate hammers SetGoal/ClearGoal from several goroutines
// while another goroutine drives ProcessInput (the real drain-loop gate) and a
// third drives armGoalContinuation directly. It mirrors session_sync_race_test.go
// and is meant to run under -race.
//
// The coordination under test is the §7 in-turn flag: SetGoal/ClearGoal touch the
// goal store (its own mutex) and read goalInTurn (s.mu); the gate runs
// armGoalContinuation (goal mutex) and the drain loop clears goalInTurn (s.mu).
// The terminal invariant after everything settles is simply that the goal store
// is in one of its legal states (no goal, active, complete, or blocked) and that
// nothing panicked or deadlocked.
func TestGoal_NoRaceSetClearVsGate(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// Each turn finishes immediately via the result tool, so the ProcessInput
	// driver loops fast and overlaps the setters. fakeAdapter falls back to a
	// canned "done" response once its (empty) script is exhausted.
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// A non-blocking kick callback: the idle-kick path must never block the
	// hammering goroutine, so we just count invocations.
	var kickMu sync.Mutex
	var kicks int
	sess.SetKickFunc(func(prompt string) {
		kickMu.Lock()
		kicks++
		kickMu.Unlock()
	})

	stop := make(chan struct{})
	var drivers sync.WaitGroup

	// Driver 1: real ProcessInput turns (exercises the drain-loop gate + the
	// goalInTurn entry/exit flag).
	drivers.Add(1)
	go func() {
		defer drivers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = sess.ProcessInput(context.Background(), "go", nil)
		}
	}()

	// Driver 2: the gate's arming step, called directly to hammer the goal mutex
	// from the turn-goroutine side independent of a full turn.
	drivers.Add(1)
	go func() {
		defer drivers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = sess.armGoalContinuation(true)
		}
	}()

	// Hammerers: SetGoal / ClearGoal from the appwire-goroutine side.
	var hammer sync.WaitGroup
	for _, fn := range []func(){
		func() { _, _ = sess.SetGoal(context.Background(), "race objective") },
		func() { sess.ClearGoal() },
	} {

		hammer.Add(1)
		go func() {
			defer hammer.Done()
			for i := 0; i < 300; i++ {
				fn()
			}
		}()
	}

	hammer.Wait()
	close(stop)
	drivers.Wait()

	// Terminal invariant: whatever the final state, it must be one of the legal
	// goal states. (No goal at all is legal — a ClearGoal may have won the last
	// race.) The point of the test is that we reach here at all: no panic, no
	// deadlock, -race clean.
	if snap, ok := sess.getOrCreateGoalStore().Snapshot(); ok {
		switch snap.Status {
		case goal.StatusActive, goal.StatusComplete, goal.StatusBlocked:
			// legal
		default:
			t.Fatalf("goal settled in an illegal status %q", snap.Status)
		}
	}

	// Touch the kick counter so the race detector covers the callback's shared
	// state too; the exact value is nondeterministic and not asserted.
	kickMu.Lock()
	_ = kicks
	kickMu.Unlock()
}
