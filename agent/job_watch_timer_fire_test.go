package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
)

func newTimerTestJM(t *testing.T) (*jobManager, *agenttest.FakeClock, chan jobNotification) {
	t.Helper()
	got := make(chan jobNotification, 64)
	jm, err := newJobManagerNoSync(t.TempDir(), testOwnerSessionID, func(n jobNotification) { got <- n })
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	clk := agenttest.NewFakeClockAt(time.Unix(1_700_000_000, 0))
	jm.clock = clk
	t.Cleanup(func() { jm.close() })
	return jm, clk, got
}

func recvNotification(t *testing.T, got chan jobNotification) jobNotification {
	t.Helper()
	select {
	case n := <-got:
		return n
	// TRIPWIRE: the notification is the real completion signal and arrives as
	// soon as the tick the test just advanced onto is served, normally in
	// microseconds. 5s only fires on a genuine hang.
	case <-time.After(5 * time.Second):
		t.Fatal("no notification delivered")
		return jobNotification{}
	}
}

func TestRepeatTimer_FiresEveryIntervalWithNote(t *testing.T) {
	jm, clk, got := newTimerTestJM(t)
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60, Note: "check the deploy"})
	if err != nil {
		t.Fatal(err)
	}
	clk.BlockUntil(1)
	for i := 1; i <= 3; i++ {
		clk.Advance(60 * time.Second)
		n := recvNotification(t, got)
		if !n.isWatch() || n.Reason != "repeat" || n.WatchID != res.WatchID || n.Note != "check the deploy" || n.IntervalSeconds != 60 || n.Fires != 1 || n.Terminal {
			t.Fatalf("tick %d = %+v", i, n)
		}
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !ok || cfg.deliveries != 3 {
		t.Fatalf("deliveries = %d (ok=%v), want 3", cfg.deliveries, ok)
	}
}

func TestOneShotTimer_FiresOnceAndEndsWithReasonFired(t *testing.T) {
	jm, clk, got := newTimerTestJM(t)
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 600, Note: "job_x should be done"})
	if err != nil {
		t.Fatal(err)
	}
	clk.BlockUntil(1)
	clk.Advance(600 * time.Second)
	n := recvNotification(t, got)
	if n.Reason != "after" || !n.Terminal || n.IntervalSeconds != 600 || n.Note != "job_x should be done" {
		t.Fatalf("one-shot notification = %+v", n)
	}
	jm.mu.Lock()
	_, _, live := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if live {
		t.Fatal("one-shot must end after firing")
	}
	clk.Advance(600 * time.Second)
	select {
	case extra := <-got:
		t.Fatalf("one-shot fired twice: %+v", extra)
	// TRIPWIRE: Advance fires the tick synchronously, so a still-armed timer
	// would deliver in microseconds. 200ms is the quiescence window that gives
	// a wrong implementation room to show itself, not a tuned expectation.
	case <-time.After(200 * time.Millisecond):
	}
	recent := jm.recentWatchSummaries()
	found := false
	for _, r := range recent {
		if r.ID == res.WatchID && r.EndReason == "fired" && r.Deliveries == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("history lacks a fired row with deliveries 1: %+v", recent)
	}
}

func TestOneShotTimer_ClearBeforeDeadlineLeavesNoTimer(t *testing.T) {
	jm, clk, got := newTimerTestJM(t)
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	clk.BlockUntil(1)
	if _, err := jm.clearWatchByID(res.WatchID); err != nil {
		t.Fatal(err)
	}
	clk.Advance(120 * time.Second)
	select {
	case n := <-got:
		t.Fatalf("cleared one-shot fired: %+v", n)
	// TRIPWIRE: Advance fires the tick synchronously, so a timer that survived
	// the clear would deliver in microseconds. 200ms is the quiescence window
	// that gives a wrong implementation room to show itself.
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPeriodicTicks_DoNotTripTheDeliveryBudget(t *testing.T) {
	jm, clk, got := newTimerTestJM(t)
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	clk.BlockUntil(1)
	for range watchDeliveryBudget + 5 {
		clk.Advance(60 * time.Second)
		recvNotification(t, got)
	}
	jm.mu.Lock()
	_, _, live := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !live {
		t.Fatal("a timer must survive past 50 ticks; the budget bounds condition fires only")
	}
}
