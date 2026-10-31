package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	clockpkg "primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

// progressSnapshotClock observes the real progress goroutine at the clock
// boundary. Its deferred Stop joins the last callback, unlike aggregate fake
// clock counts, which also include independent Session timers.
type progressSnapshotClock struct {
	*agenttest.FakeClock
	started chan struct{}
	stopped chan struct{}
}

func (c *progressSnapshotClock) NewTicker(d time.Duration) clockpkg.Ticker {
	ticker := &progressSnapshotTicker{Ticker: c.FakeClock.NewTicker(d), stopped: c.stopped}
	close(c.started)
	return ticker
}

type progressSnapshotTicker struct {
	clockpkg.Ticker
	stopped chan struct{}
}

func (t *progressSnapshotTicker) Stop() {
	t.Ticker.Stop()
	close(t.stopped)
}

// TestW2Conc_FireProgressTickClosingBails pins the closing guard: a progress
// tick that fires while the manager is closing does nothing and reports false.
func TestW2Conc_FireProgressTickClosingBails(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now(), "")
	if err != nil {
		t.Fatalf("newWatchConfig: %v", err)
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID}
	jm.mu.Lock()
	jm.watches[key] = cfg
	jm.closing = true
	jm.mu.Unlock()

	if jm.fireProgressTick(key, cfg) {
		t.Fatal("fireProgressTick returned true while closing, want false")
	}
}

// TestW2Conc_FireProgressTickStaleConfigBails pins the config-mismatch guard: a
// tick whose config no longer occupies its key (cleared/replaced) bails false.
func TestW2Conc_FireProgressTickStaleConfigBails(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now(), "")
	if err != nil {
		t.Fatalf("newWatchConfig: %v", err)
	}
	// Never installed into jm.watches, so jm.watches[key] != cfg.
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID}
	if jm.fireProgressTick(key, cfg) {
		t.Fatal("fireProgressTick returned true for a config absent from its key, want false")
	}
}

// TestW2Conc_FireProgressTickJobTargetGoneBails pins the dead-target guard: a
// job-targeted tick whose job is no longer running bails false.
func TestW2Conc_FireProgressTickJobTargetGoneBails(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	cfg, err := newWatchConfig(watchArgs{Target: "job_gone", ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now(), "")
	if err != nil {
		t.Fatalf("newWatchConfig: %v", err)
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "job_gone"}
	jm.mu.Lock()
	jm.watches[key] = cfg
	jm.mu.Unlock()

	if jm.fireProgressTick(key, cfg) {
		t.Fatal("fireProgressTick returned true for a non-running job target, want false")
	}
}

// TestW2Conc_FireProgressTickAtBudgetStaysInstalled pins the periodic-tick
// accounting arm: a notification tick that reaches the per-watch delivery
// budget counts its delivery but does NOT trip the circuit breaker. The budget
// bounds condition fires; a periodic tick (job progress or a timer) is a clock,
// not a condition, so a watch that only ever ticks stays installed.
func TestW2Conc_FireProgressTickAtBudgetStaysInstalled(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now(), "")
	if err != nil {
		t.Fatalf("newWatchConfig: %v", err)
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID}
	jm.mu.Lock()
	jm.watches[key] = cfg
	cfg.deliveries = watchDeliveryBudget - 1 // next delivery reaches the budget
	jm.mu.Unlock()

	if !jm.fireProgressTick(key, cfg) {
		t.Fatal("fireProgressTick returned false for a live watch, want true")
	}
	if cfg.deliveries != watchDeliveryBudget {
		t.Fatalf("deliveries = %d, want %d (budget reached)", cfg.deliveries, watchDeliveryBudget)
	}
	if len(notified) != 1 {
		t.Fatalf("notifications = %+v, want the one progress tick", notified)
	}
	jm.mu.Lock()
	_, stillInstalled := jm.watches[key]
	jm.mu.Unlock()
	if !stillInstalled {
		t.Fatal("a periodic tick at the budget auto-cleared the watch; the budget bounds condition fires only")
	}
}

// TestW2Conc_FireProgressTickSendPathSnapshots pins the send (watch_send) arm:
// a tick on a send-configured watch (not suppressed) snapshots a delivery and
// records it as pending before canonical receiver custody settles that same
// delivery. Custody alone is not provider observation or consumption.
func TestW2Conc_FireProgressTickSendPathSnapshots(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai"}
	const payload = "progress-payload-84f0"
	adapter.steps = []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			observationRequest(t, req, []string{payload}, nil)
			return finalResponse("progress-observed-84f0")
		},
	}
	clock := &progressSnapshotClock{FakeClock: agenttest.NewFakeClock(), started: make(chan struct{}), stopped: make(chan struct{})}
	s := newSession(t, withAdapter(adapter), withConfig(SessionConfig{StateDir: t.TempDir(), clock: clock}), withoutGitSnapshot())
	jm := s.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "test cleanup", nil); err != nil {
			t.Error(err)
		}
	}()
	res, err := jm.configureWatch(watchArgs{
		Target:             rec.JobID,
		ProgressIntervalMS: minWatchProgressIntervalMS,
		Send:               &watchSendArgs{To: runtimeMessageAliasCaller, Message: payload},
	})
	if err != nil {
		t.Fatal(err)
	}
	watchStopped := false
	stopWatch := func() {
		if watchStopped {
			return
		}
		watchStopped = true
		if _, err := jm.clearWatchByID(res.WatchID); err != nil {
			t.Error(err)
		}
		// The real progress goroutine defers Stop until its last callback returns.
		waitForTestSignal(t, clock.stopped, "progress ticker to stop")
	}
	defer stopWatch()
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !ok {
		t.Fatal("progress registration missing")
	}
	waitForTestSignal(t, clock.started, "progress ticker registration")
	// This call returns after the real owned receiver has accepted custody.
	// Keep acceptance distinct from the later successful provider response.
	if !jm.fireProgressTick(key, cfg) {
		t.Fatal("fireProgressTick returned false for a live send watch, want true")
	}
	pending := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	delivered := producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)
	dropped := producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)
	if len(pending) != 1 || len(delivered) != 1 || len(dropped) != 0 {
		t.Fatalf("source journal pending/delivered/dropped=%d/%d/%d, want 1/1/0", len(pending), len(delivered), len(dropped))
	}
	state := pending[0].WatchSend
	if state == nil || delivered[0].WatchSend == nil || state.DeliveryID == "" || state.DeliveryID != delivered[0].WatchSend.DeliveryID || state.TriggerReason != "progress_tick" || state.ReceiverSessionID != s.ID() {
		t.Fatal("source progress snapshot and canonical settlement identity do not match")
	}
	id := stableWatchAttentionID(*state)
	assertNotificationPending(t, s, []string{id})
	if got := len(loadWatchSendRecord(t, jm).Pending); got != 0 {
		t.Fatalf("source still pending after custody: %d", got)
	}
	accepted := 0
	for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
		if entry.Turn.AttentionID == id {
			accepted++
		}
		if len(entry.Turn.ObservedNotificationIDs) != 0 || entry.Turn.AttentionResolution != nil {
			t.Fatal("custody was incorrectly treated as provider observation")
		}
	}
	if accepted != 1 || len(adapter.Requests()) != 0 {
		t.Fatalf("before provider accepted=%d requests=%d, want 1/0", accepted, len(adapter.Requests()))
	}
	stopWatch()
	clock.Advance(2 * time.Duration(minWatchProgressIntervalMS) * time.Millisecond)
	clock.Drain()
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if len(adapter.Requests()) != 1 {
		t.Fatalf("provider requests=%d, want one observation", len(adapter.Requests()))
	}
	assertNotificationPending(t, s, []string{})
	requireOwnedDrainAttentionObservedOnce(t, s, id)
	jm.dispatchProducedWatchSends()
	if producerRecordCount(t, s) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 {
		t.Fatal("stopped timer or repeated transport duplicated or dropped the settled progress frame")
	}
}
