package agent

import (
	"testing"

	"primeradiant.com/serf/agent/provenance"
)

// TestW2Conc_FireProgressTickClosingBails pins the closing guard: a progress
// tick that fires while the manager is closing does nothing and reports false.
func TestW2Conc_FireProgressTickClosingBails(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now())
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
	cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now())
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
	cfg, err := newWatchConfig(watchArgs{Target: "job_gone", ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now())
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

// TestW2Conc_FireProgressTickOverBudgetAutoClears pins the over-budget arm: a
// notification tick that crosses the per-watch delivery budget records the
// notification and auto-clears the watch (circuit breaker, spec §4 F1).
func TestW2Conc_FireProgressTickOverBudgetAutoClears(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now())
	if err != nil {
		t.Fatalf("newWatchConfig: %v", err)
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID}
	jm.mu.Lock()
	jm.watches[key] = cfg
	cfg.deliveries = watchDeliveryBudget - 1 // next delivery crosses the budget
	jm.mu.Unlock()

	if !jm.fireProgressTick(key, cfg) {
		t.Fatal("fireProgressTick returned false for a live watch, want true")
	}
	if cfg.deliveries != watchDeliveryBudget {
		t.Fatalf("deliveries = %d, want %d (budget crossed)", cfg.deliveries, watchDeliveryBudget)
	}
	jm.mu.Lock()
	_, stillInstalled := jm.watches[key]
	jm.mu.Unlock()
	if stillInstalled {
		t.Fatal("over-budget watch was not auto-cleared from jm.watches")
	}
}

// TestW2Conc_FireProgressTickSendPathSnapshots pins the send (watch_send) arm:
// a tick on a send-configured watch (not suppressed) snapshots a delivery and
// records it as pending rather than enqueuing a notification.
func TestW2Conc_FireProgressTickSendPathSnapshots(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:             rec.JobID,
		ProgressIntervalMS: minWatchProgressIntervalMS,
		Send:               &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "dlg_obs"}

	// A non-same-watch provenance so the tick is not suppressed and takes the
	// send path.
	jm.mu.Lock()
	jm.running[rec.JobID].rec.Provenance = provenance.Clone(nil)
	jm.mu.Unlock()

	if !jm.fireProgressTick(key, cfg) {
		t.Fatal("fireProgressTick returned false for a live send watch, want true")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("send-path tick pending = %+v, want one recorded delivery", pending)
	}
}
