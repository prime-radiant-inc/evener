package agent

import (
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// Clearing a live watch appends its WatchCleared teardown durably. When that
// append fails the clear returns the error and the watch config stays reachable
// (the rejecting mark is rolled back), so the model can retry.
func TestW2Watch_clearWatchByIDMatchingTeardownAppendError(t *testing.T) {
	jm := newTestJM(t)
	res, err := jm.configureWatch(watchArgs{Target: "*", Events: []string{"job.notification"}})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	watchID := res.WatchID
	if watchID == "" {
		t.Fatal("install produced no watch id")
	}

	realAppendEvents := jm.appendEvents
	appendErr := errors.New("teardown append failed")
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventWatchCleared {
				return appendErr
			}
		}
		return realAppendEvents(events)
	}

	if _, err := jm.clearWatchByIDMatching(watchID, func(*watchConfig) bool { return true }, false); !errors.Is(err, appendErr) {
		t.Fatalf("clear error = %v, want the teardown append failure", err)
	}

	jm.mu.Lock()
	_, _, stillLive := jm.watchConfigByIDLocked(watchID)
	rejecting := false
	for _, cfg := range jm.watches {
		if cfg.watchID == watchID {
			rejecting = cfg.rejectingDelivery
		}
	}
	jm.mu.Unlock()
	if !stillLive {
		t.Fatal("watch config was torn down after a failed teardown append")
	}
	if rejecting {
		t.Fatal("watch config stayed rejecting after a failed teardown append")
	}
}

// A durable clear for an unknown watch id loads the durable watch table to mint
// its WatchCleared event; when that load fails the store error propagates.
func TestW2Watch_clearWatchByIDDurableLoadError(t *testing.T) {
	jm := newTestJM(t)
	if _, err := jm.createShell(createShellOpts{Command: "x"}); err != nil {
		t.Fatalf("createShell: %v", err)
	}
	s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))

	if _, err := jm.clearWatchByID("w_unknown"); err == nil {
		t.Fatal("durable clear over a corrupt store succeeded, want the load error")
	}
}
