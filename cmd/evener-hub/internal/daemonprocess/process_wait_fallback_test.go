package daemonprocess

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// pollCountingProcess is the shape Darwin's handle has: an inspection that
// answers "has it exited" and no exit signal to park on. It counts the
// questions, which is the fallback's whole cost.
type pollCountingProcess struct {
	kernelProcess
	questions atomic.Int32
}

func (c *pollCountingProcess) exited() (bool, error) {
	c.questions.Add(1)
	return c.gone, nil
}

func openPollCounting(t *testing.T) (*pollCountingProcess, Process) {
	t.Helper()
	c := &pollCountingProcess{}
	c.facts = validIdentity()
	p, err := (controller{bind: func(int) (processHandle, error) { return c, nil }}).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

// fallbackQuestionCeilingPerSecond is the most exit questions a fallback waiter
// may ask per second of a live process. The interval costs about 51 today; the
// ceiling leaves headroom for a tick landing either side of a deadline while
// still failing a waiter that spins or that shortened its interval.
const fallbackQuestionCeilingPerSecond = 60

// TestWaitFallbackAsksOncePerInterval bounds what the polling fallback costs on
// a platform whose handle cannot signal. Such a waiter re-asks the process once
// per interval: one question to settle the exit before parking, then one per
// interval spent. The window is a whole number of intervals plus a half, so the
// context deadline and a tick are never ready in the same select.
func TestWaitFallbackAsksOncePerInterval(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c, p := openPollCounting(t)
		defer p.Close()
		window := 10*fallbackExitPollInterval + fallbackExitPollInterval/2
		ctx, cancel := context.WithTimeout(context.Background(), window)
		defer cancel()
		if err := p.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Wait over a live process: %v, want context.DeadlineExceeded", err)
		}
		// The shape: one question per interval spent, not one per scheduler pass.
		want := int(window/fallbackExitPollInterval) + 1
		if got := int(c.questions.Load()); got != want {
			t.Fatalf("fallback asked %d times over %v, want %d (one per interval, no busy loop)", got, window, want)
		}
		// The absolute cost, independent of the interval the code names, so a
		// shortened interval fails even though want would shrink with it.
		ceiling := int(fallbackQuestionCeilingPerSecond*window/time.Second) + 1
		if got := int(c.questions.Load()); got > ceiling {
			t.Fatalf("fallback asked %d questions over %v, above the %d allowed by the %d-per-second ceiling", got, window, ceiling, fallbackQuestionCeilingPerSecond)
		}
		t.Logf("fallback asked %d questions over %v (%v per question)", c.questions.Load(), window, fallbackExitPollInterval)
	})
}

// TestWaitFallbackReturnsWhenTheExitPrecededIt is the ordering a poll must not
// lose: a waiter that registers after the condition already became true answers
// from its first question and never parks. Move the exit check after the park
// and this wait stalls for an interval against an exit that had already
// happened.
func TestWaitFallbackReturnsWhenTheExitPrecededIt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c, p := openPollCounting(t)
		defer p.Close()
		c.gone = true
		start := time.Now()
		if err := p.Wait(context.Background()); err != nil {
			t.Fatalf("Wait registered after the exit: %v", err)
		}
		// No interval may pass: a park here is the wait missing an exit that had
		// already happened. Fake time makes that visible as clock advance.
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("a wait registered after the exit spent %v before returning", elapsed)
		}
		if got := c.questions.Load(); got != 1 {
			t.Fatalf("a wait registered after the exit asked %d times, want 1", got)
		}
	})
}

// TestWaitFallbackEndsOnClose pins that a Close is a wake for a parked fallback
// waiter, so the waiter never has to observe the closed descriptor through a
// later question.
func TestWaitFallbackEndsOnClose(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		_, p := openPollCounting(t)
		done := make(chan error, 1)
		go func() { done <- p.Wait(context.Background()) }()
		synctest.Wait() // the waiter asked and is parked
		start := time.Now()
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("a wait ended successfully on a Close that reported no exit")
			}
		case <-time.After(time.Second): // TRIPWIRE: only fires on a lost wake.
			t.Fatal("Close did not end the parked wait")
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("the Close took %v to end the wait: it waited for a poll interval", elapsed)
		}
	})
}

// TestWaitFallbackAfterCloseReturnsAtOnce is the gap between the condition
// check and the park. The close happened before this wait began, so the first
// check must observe it and return without ever parking. A waiter that checked
// only for an exit and then parked would spend an interval on a close it had
// already passed.
func TestWaitFallbackAfterCloseReturnsAtOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c, p := openPollCounting(t)
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := p.Wait(context.Background()); err == nil {
			t.Fatal("a closed process was treated as an exit")
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("a wait begun after the Close spent %v before returning", elapsed)
		}
		if got := c.questions.Load(); got != 0 {
			t.Fatalf("a wait begun after the Close asked the process %d times, want 0", got)
		}
	})
}
