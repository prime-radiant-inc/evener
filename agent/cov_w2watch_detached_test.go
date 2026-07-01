package agent

import (
	"errors"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// w2watch_seedDetachedPending installs a detached (terminal-flush) watch config
// holding one caller-facing pending watch-send for target/sendTo. This is the
// residue a torn-down watch leaves behind, and the shape configureWatch and
// clearWatchByIDMatching must drain when a new config or a clear lands on the
// same key.
func w2watch_seedDetachedPending(t *testing.T, jm *jobManager, watchID, target, sendTo string) *watchConfig {
	t.Helper()
	pendingKey := jobstore.WatchSendKey{
		VisibleSessionID:        jm.sessionID,
		WatchTarget:             target,
		ResolvedWatchedIdentity: target,
		ResolvedSendTo:          sendTo,
		WatchGeneration:         jobstore.NewWatchGeneration(),
	}
	detached := &watchConfig{
		watchID:    watchID,
		target:     target,
		send:       &watchSendArgs{To: sendTo, Message: "observe"},
		generation: pendingKey.WatchGeneration,
		pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{
			pendingKey: {
				Key:           pendingKey,
				DeliveryID:    "watch_delivery_" + watchID,
				UpdateSeq:     1,
				Message:       "observe",
				TriggerReason: "output_match: ready",
			},
		},
		pendingOrder: []jobstore.WatchSendKey{pendingKey},
	}
	jm.mu.Lock()
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[detached] = true
	jm.mu.Unlock()
	return detached
}

func w2watch_failAppendEventsOnKind(jm *jobManager, kind jobstore.EventKind, err error) {
	real := jm.appendEvents
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, e := range events {
			if e.Kind == kind {
				return err
			}
		}
		return real(events)
	}
}

// Clearing a watch id that has no live config but does have a detached pending
// drains that pending durably; when the drain append fails the clear returns the
// error and leaves the detached config reachable for retry (rejecting rolled back).
func TestW2Watch_clearWatchByIDMatchingDetachedAppendError(t *testing.T) {
	jm := newTestJM(t)
	const watchID = "w_detached"
	detached := w2watch_seedDetachedPending(t, jm, watchID, "job_x", "dlg_obs")

	appendErr := errors.New("detached drain append failed")
	w2watch_failAppendEventsOnKind(jm, jobstore.EventWatchSendDropped, appendErr)

	if _, err := jm.clearWatchByIDMatching(watchID, func(*watchConfig) bool { return true }, false); !errors.Is(err, appendErr) {
		t.Fatalf("clear error = %v, want the detached drain append failure", err)
	}

	jm.mu.Lock()
	reachable := jm.terminalFlush[detached]
	rejecting := detached.rejectingDelivery
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("detached config forgotten after a failed drain append")
	}
	if rejecting {
		t.Fatal("detached config stayed rejecting after a failed drain append")
	}
}

// A NEW watch install that lands on a key still holding a detached pending drains
// that pending first; when that drain append fails the install aborts with the
// error and no live watch is registered.
func TestW2Watch_configureWatchNewWithDetachedAppendError(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "sleep 30"})
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	w2watch_seedDetachedPending(t, jm, "w_old", rec.JobID, "dlg_obs")

	// The new-watch detached drain writes its dropped tombstones one at a time via
	// appendEvent, so fault that seam rather than the batch seam.
	failAppendN(jm, jobstore.EventWatchSendDropped, 1)

	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err == nil {
		t.Fatal("install succeeded, want the detached snapshot append failure")
	}

	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "dlg_obs"}
	jm.mu.Lock()
	_, installed := jm.watches[key]
	jm.mu.Unlock()
	if installed {
		t.Fatal("watch registered despite the detached snapshot append failure")
	}
}

// An IDEMPOTENT re-configure (config hash unchanged) that finds a detached pending
// on the same key drains that residue; when the drain append fails the reconfigure
// returns the error and the existing live watch is untouched.
func TestW2Watch_configureWatchEqualWithDetachedAppendError(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "sleep 30"})
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	args := watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}
	res, err := jm.configureWatch(args)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	firstID := res.WatchID

	w2watch_seedDetachedPending(t, jm, "w_old", rec.JobID, "dlg_obs")

	appendErr := errors.New("equal-path detached drain append failed")
	w2watch_failAppendEventsOnKind(jm, jobstore.EventWatchSendDropped, appendErr)

	if _, err := jm.configureWatch(args); !errors.Is(err, appendErr) {
		t.Fatalf("idempotent reconfigure error = %v, want the drain append failure", err)
	}

	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "dlg_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil || cfg.watchID != firstID {
		t.Fatalf("existing watch changed after failed drain: %+v (want %s intact)", cfg, firstID)
	}
}
