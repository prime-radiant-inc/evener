package agent

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
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
	if !ok {
		t.Fatalf("repeat timer %s left the live set", res.WatchID)
	}
	if cfg.deliveries != 3 {
		t.Fatalf("deliveries = %d, want 3", cfg.deliveries)
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

// TestConditionFireBudget_TicksDoNotDisarmTheBreaker pins what the delivery
// budget counts. A watch that both ticks and matches output survives more ticks
// than the budget and still auto-clears on its 50th condition fire. Anchoring
// the latch on deliveries instead would let the ticks step over the crossing
// and leave the circuit breaker permanently disarmed for that watch.
func TestConditionFireBudget_TicksDoNotDisarmTheBreaker(t *testing.T) {
	jm := newTestJM(t)
	// The fake clock leaves the watch's background progress timer inert; the
	// ticks below are driven synchronously, doing exactly that goroutine's work.
	jm.clock = agenttest.NewFakeClockAt(time.Unix(1_700_000_000, 0))
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	res, err := jm.configureWatch(watchArgs{
		Operation:          "create",
		Source:             rec.JobID,
		Target:             rec.JobID,
		OutputMatch:        "hit",
		ProgressIntervalMS: minWatchProgressIntervalMS,
	})
	if err != nil {
		t.Fatalf("configureWatch: %v", err)
	}
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !ok {
		t.Fatalf("watch %s is not installed", res.WatchID)
	}

	for tick := 1; tick <= watchDeliveryBudget+5; tick++ {
		if !jm.fireProgressTick(key, cfg) {
			t.Fatalf("progress tick %d ended the watch", tick)
		}
	}
	jm.mu.Lock()
	deliveries, fires := cfg.deliveries, cfg.conditionFires
	_, _, live := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !live || deliveries != watchDeliveryBudget+5 || fires != 0 {
		t.Fatalf("after ticks: live=%v deliveries=%d conditionFires=%d, want a live watch with %d deliveries and no condition fires",
			live, deliveries, fires, watchDeliveryBudget+5)
	}

	var offset int64
	for fire := 1; fire <= watchDeliveryBudget; fire++ {
		chunk := []byte("hit\n")
		offset += int64(len(chunk))
		jm.feedJobOutput(rec.JobID, chunk, offset)
		jm.mu.Lock()
		_, _, live = jm.watchConfigByIDLocked(res.WatchID)
		jm.mu.Unlock()
		if want := fire < watchDeliveryBudget; live != want {
			t.Fatalf("after condition fire %d: live=%v, want %v", fire, live, want)
		}
	}
}

// TestConditionFireBudget_CrossingLatchesOnceAcrossASkippedBudget pins the
// breaker's once-latch against the send rail's snapshot/settle skew. A send
// watch counts a condition fire when it snapshots a frame and lands in
// recordWatchDeliveryLocked when that frame settles, so two fires snapshotted
// before either settles walk conditionFires straight from 49 to 51. An equality
// test would step over the budget and disarm the breaker forever; "at or past
// the budget", latched once per config, reports the crossing exactly once.
func TestConditionFireBudget_CrossingLatchesOnceAcrossASkippedBudget(t *testing.T) {
	jm := newTestJM(t)
	cfg := &watchConfig{conditionFires: watchDeliveryBudget - 1}

	if jm.recordWatchDeliveryLocked(cfg) {
		t.Fatalf("settle at %d condition fires crossed the budget early", cfg.conditionFires)
	}
	// Two more fires snapshot before the next settle, skipping the budget itself.
	cfg.conditionFires = watchDeliveryBudget + 1
	if !jm.recordWatchDeliveryLocked(cfg) {
		t.Fatalf("settle at %d condition fires did not cross the budget", cfg.conditionFires)
	}
	cfg.conditionFires = watchDeliveryBudget + 2
	if jm.recordWatchDeliveryLocked(cfg) {
		t.Fatalf("settle at %d condition fires crossed the budget a second time", cfg.conditionFires)
	}
	if cfg.deliveries != 3 || !cfg.budgetTripped {
		t.Fatalf("deliveries = %d, budgetTripped = %v; want 3 and a latched breaker", cfg.deliveries, cfg.budgetTripped)
	}
}

// TestConditionFireBudget_FailedTeardownRearmsTheBreaker pins the once-only
// latch against a durable teardown that does not persist. The failed teardown
// rolls the watch back into the live set still over budget, so the latch must
// be re-armed: left set, no later condition fire reports a crossing and the
// watch delivers past its budget forever.
func TestConditionFireBudget_FailedTeardownRearmsTheBreaker(t *testing.T) {
	jm := newTestJM(t)
	// The failing append is installed before the watched job exists, so the
	// job's own output pump never races the test over jm.appendEvents.
	var mu sync.Mutex
	teardownFails := true
	teardownErr := errors.New("budget teardown append failed")
	realAppendEvents := jm.appendEvents
	jm.appendEvents = func(evs []jobstore.Event) error {
		mu.Lock()
		fails := teardownFails
		mu.Unlock()
		for _, ev := range evs {
			if fails && ev.Kind == jobstore.EventWatchCleared {
				return teardownErr
			}
		}
		return realAppendEvents(evs)
	}

	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: rec.JobID, Target: rec.JobID, OutputMatch: "hit"})
	if err != nil {
		t.Fatalf("configureWatch: %v", err)
	}

	var offset int64
	fire := func() {
		chunk := []byte("hit\n")
		offset += int64(len(chunk))
		jm.feedJobOutput(rec.JobID, chunk, offset)
	}
	for i := 1; i <= watchDeliveryBudget; i++ {
		fire()
	}
	jm.mu.Lock()
	_, _, live := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !live {
		t.Fatal("a teardown that did not persist still dropped the watch from the live set")
	}

	mu.Lock()
	teardownFails = false
	mu.Unlock()
	fire()
	jm.mu.Lock()
	_, _, live = jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if live {
		t.Fatal("the condition fire after a failed teardown did not retry the over-budget auto-clear")
	}
	history := jm.recentWatchSummaries()
	if len(history) == 0 || history[0].ID != res.WatchID || history[0].EndReason != "budget_exhausted" {
		t.Fatalf("watch history = %+v, want %s ended budget_exhausted", history, res.WatchID)
	}
}

// TestConditionFireBudget_UnfiredWatchExcuseFollowsConditionFires pins which
// counter excuses a watched job from the undisposed-background-job
// announcement. The excuse is "this watch has not matched yet", plus a handoff
// window for a watch whose breaker just tripped. A watch with a progress
// interval delivers past the budget forever without ever tripping it, so keying
// the window on deliveries would excuse it permanently once it had fired.
func TestConditionFireBudget_UnfiredWatchExcuseFollowsConditionFires(t *testing.T) {
	jm := newTestJM(t)
	const jobID = "job_unfired_excuse"
	key := watchKey{VisibleSessionID: jm.sessionID, Target: jobID}
	cfg := &watchConfig{target: jobID}
	jm.mu.Lock()
	jm.watches[key] = cfg
	jm.mu.Unlock()

	if !jm.hasLiveUnfiredWatchOnTarget(jobID) {
		t.Fatal("a watch that has never matched must excuse its target")
	}

	// Fired once, then ticked well past the budget: no longer unfired, and its
	// breaker has not tripped, so it is not an excuse.
	jm.mu.Lock()
	cfg.conditionFires = 1
	cfg.deliveries = watchDeliveryBudget + 10
	jm.mu.Unlock()
	if jm.hasLiveUnfiredWatchOnTarget(jobID) {
		t.Fatalf("a watch with %d tick deliveries and one match still excused its target", cfg.deliveries)
	}

	// At the budget in condition fires: the teardown handoff window is open.
	jm.mu.Lock()
	cfg.conditionFires = watchDeliveryBudget
	jm.mu.Unlock()
	if !jm.hasLiveUnfiredWatchOnTarget(jobID) {
		t.Fatal("a watch at the condition-fire budget must hold the announcement while its teardown lands")
	}
}

// TestOneShotTimer_FailedTeardownWarnsThroughTheSession pins where a one-shot's
// failed durable teardown is reported. The job manager's warning convention is
// jm.emit, which reaches the session and the hub; stderr reaches neither. The
// fire itself is still delivered — the teardown failure does not swallow it.
func TestOneShotTimer_FailedTeardownWarnsThroughTheSession(t *testing.T) {
	jm, clk, got := newTimerTestJM(t)
	var mu sync.Mutex
	var warnings []events.WarningData
	jm.emit = func(kind events.EventKind, data events.EventData, _ *provenance.Causal) {
		if kind != events.EventWarning {
			return
		}
		w, ok := data.(events.WarningData)
		if !ok {
			return
		}
		mu.Lock()
		warnings = append(warnings, w)
		mu.Unlock()
	}
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	realAppendEvents := jm.appendEvents
	teardownErr := errors.New("one-shot teardown append failed")
	jm.appendEvents = func(evs []jobstore.Event) error {
		for _, ev := range evs {
			if ev.Kind == jobstore.EventWatchCleared {
				return teardownErr
			}
		}
		return realAppendEvents(evs)
	}

	clk.BlockUntil(1)
	clk.Advance(60 * time.Second)
	if n := recvNotification(t, got); n.Reason != "after" {
		t.Fatalf("one-shot fire = %+v, want the fire delivered despite the failed teardown", n)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0].Message, res.WatchID) || !strings.Contains(warnings[0].Message, teardownErr.Error()) {
		t.Fatalf("warning message = %q, want the watch id and the store error", warnings[0].Message)
	}
}

// TestOneShotTimer_FailedTeardownRetriesOnTheNextTick pins the ghost-timer case.
// A one-shot whose durable teardown does not persist stays registered, so its
// ticker must stay armed and retry the end on the next tick; stopping the ticker
// would leave a live watch nothing can ever end. The retry is an end, not a
// second fire — the model hears about the timer exactly once.
func TestOneShotTimer_FailedTeardownRetriesOnTheNextTick(t *testing.T) {
	jm, clk, got := newTimerTestJM(t)
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	teardownFails := true
	realAppendEvents := jm.appendEvents
	jm.appendEvents = func(evs []jobstore.Event) error {
		mu.Lock()
		fails := teardownFails
		mu.Unlock()
		for _, ev := range evs {
			if fails && ev.Kind == jobstore.EventWatchCleared {
				return errors.New("one-shot teardown append failed")
			}
		}
		return realAppendEvents(evs)
	}

	clk.BlockUntil(1)
	clk.Advance(60 * time.Second)
	if n := recvNotification(t, got); n.Reason != "after" {
		t.Fatalf("one-shot fire = %+v, want the single fire delivered", n)
	}
	jm.mu.Lock()
	_, _, live := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !live {
		t.Fatal("a teardown that did not persist still dropped the one-shot from the live set")
	}

	mu.Lock()
	teardownFails = false
	mu.Unlock()
	clk.Advance(60 * time.Second)
	// TRIPWIRE: the retry runs on the tick this Advance just fired, so the
	// watch leaves the live set in microseconds. 5s only fires on a genuine hang.
	waitForCondition(t, 5*time.Second, "the retried one-shot teardown to end the watch", func() bool {
		jm.mu.Lock()
		defer jm.mu.Unlock()
		_, _, stillLive := jm.watchConfigByIDLocked(res.WatchID)
		return !stillLive
	})
	select {
	case extra := <-got:
		t.Fatalf("the retry tick delivered a second notification: %+v", extra)
	default:
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
