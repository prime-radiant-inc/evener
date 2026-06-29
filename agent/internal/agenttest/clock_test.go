package agenttest

import (
	"sync"
	"testing"
	"time"
)

func TestFakeClockNowAdvance(t *testing.T) {
	c := NewFakeClock()
	t0 := c.Now()
	c.Advance(5 * time.Second)
	if got := c.Now().Sub(t0); got != 5*time.Second {
		t.Fatalf("Now advanced by %v, want 5s", got)
	}
}

func TestFakeClockAfterFiresOnAdvance(t *testing.T) {
	c := NewFakeClock()
	ch := c.After(10 * time.Second)
	select {
	case <-ch:
		t.Fatal("After fired before any Advance")
	default:
	}
	c.Advance(10 * time.Second)
	select {
	case got := <-ch:
		if got != c.Now() {
			t.Fatalf("After delivered %v, want now=%v", got, c.Now())
		}
	default:
		t.Fatal("After did not fire after Advance")
	}
}

func TestFakeClockSleepBlocksUntilAdvance(t *testing.T) {
	c := NewFakeClock()
	done := make(chan struct{})
	go func() {
		c.Sleep(time.Minute)
		close(done)
	}()
	// Wait for the sleeper to park on the clock, then confirm it has not woken.
	c.BlockUntil(1)
	select {
	case <-done:
		t.Fatal("Sleep returned before Advance")
	default:
	}
	c.Advance(time.Minute)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Sleep did not return after Advance")
	}
}

func TestFakeClockTimerStop(t *testing.T) {
	c := NewFakeClock()
	tm := c.NewTimer(time.Minute)
	if !tm.Stop() {
		t.Fatal("Stop on a pending timer reported not-pending")
	}
	c.Advance(2 * time.Minute)
	select {
	case <-tm.C():
		t.Fatal("stopped timer fired")
	default:
	}
	if tm.Stop() {
		t.Fatal("second Stop reported pending")
	}
}

func TestFakeClockTimerReset(t *testing.T) {
	c := NewFakeClock()
	tm := c.NewTimer(time.Hour)
	c.Advance(time.Minute) // not yet due
	if !tm.Reset(time.Minute) {
		t.Fatal("Reset of a pending timer reported not-pending")
	}
	c.Advance(time.Minute)
	select {
	case <-tm.C():
	default:
		t.Fatal("timer did not fire after Reset + Advance")
	}
}

func TestFakeClockTickerFiresAndReschedules(t *testing.T) {
	c := NewFakeClock()
	tk := c.NewTicker(time.Second)
	defer tk.Stop()
	// Advancing 3s should produce ticks; the channel is buffered 1, so a
	// non-draining consumer sees coalesced ticks (matching time.Ticker).
	c.Advance(3 * time.Second)
	select {
	case <-tk.C():
	default:
		t.Fatal("ticker did not fire across a multi-period Advance")
	}
}

func TestFakeClockAfterFuncRuns(t *testing.T) {
	c := NewFakeClock()
	var ran sync.WaitGroup
	ran.Add(1)
	c.AfterFunc(time.Second, ran.Done)
	c.Advance(time.Second)
	doneCh := make(chan struct{})
	go func() { ran.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("AfterFunc callback did not run after Advance")
	}
}

func TestFakeClockAfterFuncStop(t *testing.T) {
	c := NewFakeClock()
	var mu sync.Mutex
	ran := false
	tm := c.AfterFunc(time.Second, func() { mu.Lock(); ran = true; mu.Unlock() })
	if !tm.Stop() {
		t.Fatal("Stop on a pending AfterFunc reported not-pending")
	}
	c.Advance(2 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	if ran {
		t.Fatal("stopped AfterFunc ran")
	}
}

func TestFakeClockBlockUntilCounts(t *testing.T) {
	c := NewFakeClock()
	if n := c.BlockedCount(); n != 0 {
		t.Fatalf("fresh clock has %d waiters, want 0", n)
	}
	go c.Sleep(time.Minute)
	go c.Sleep(time.Minute)
	c.BlockUntil(2)
	if n := c.BlockedCount(); n < 2 {
		t.Fatalf("BlockUntil(2) returned with %d waiters", n)
	}
}

func TestFakeClockDeadlineGating(t *testing.T) {
	// Each waiter fires only once virtual time reaches its deadline. (Callbacks
	// run in goroutines, so their cross-goroutine execution order is not
	// observable; the internal fire order is deadline-then-insertion, but the
	// externally meaningful guarantee — and the one the harness relies on — is
	// that nothing fires early.)
	c := NewFakeClock()
	a := c.NewTimer(1 * time.Second)
	b := c.NewTimer(2 * time.Second)
	cc := c.NewTimer(3 * time.Second)

	fired := func(tm interface{ C() <-chan time.Time }) bool {
		select {
		case <-tm.C():
			return true
		default:
			return false
		}
	}

	c.Advance(1 * time.Second)
	if !fired(a) || fired(b) || fired(cc) {
		t.Fatalf("after 1s: a should fire, b/c should not")
	}
	c.Advance(1 * time.Second)
	if fired(a) || !fired(b) || fired(cc) {
		t.Fatalf("after 2s: only b should fire")
	}
	c.Advance(1 * time.Second)
	if fired(a) || fired(b) || !fired(cc) {
		t.Fatalf("after 3s: only c should fire")
	}
}
