package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestRestoreHistoricalWatchesNormalizesCallerToSelf(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sid := "02wMz5TxvEMoJEDTDGOTi1"
	dir := jobsDir(stateDir, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Historical watch registered with Target: "caller" and cleared.
	if err := store.Append(jobstore.Event{
		Kind:    jobstore.EventWatchRegistered,
		TS:      now,
		WatchID: "watch_caller_hist",
		Watch: &jobstore.WatchEvent{
			Generation:       "wg_1",
			OwnerSessionID:   sid,
			VisibleSessionID: sid,
			Target:           "caller",
			ConfigHash:       "cfg1",
			Condition:        "assistant.tool",
			Deliveries:       1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(jobstore.Event{
		Kind:    jobstore.EventWatchCleared,
		TS:      now,
		WatchID: "watch_caller_hist",
		Watch: &jobstore.WatchEvent{
			Generation: "wg_1",
			EndReason:  "cleared",
			Deliveries: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	// Reopen job manager from disk
	jm, err := newJobManager(stateDir, sid, func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	defer jm.closeRuntimeState(t.Context())

	inspected := jm.inspectWatchByID("watch_caller_hist")
	if inspected.WatchID != "watch_caller_hist" || inspected.Watching {
		t.Fatalf("inspected = %+v, want watch_caller_hist not watching", inspected)
	}
	if inspected.Source != "self" {
		t.Fatalf("inspected.Source = %q, want self (not caller)", inspected.Source)
	}
	if inspected.Deliveries != 1 {
		t.Fatalf("inspected.Deliveries = %d, want 1", inspected.Deliveries)
	}

	recent := jm.recentWatchSummaries()
	if len(recent) != 1 {
		t.Fatalf("recentWatchSummaries returned %d entries, want 1", len(recent))
	}
	if recent[0].Source != "self" {
		t.Fatalf("recent[0].Source = %q, want self (not caller)", recent[0].Source)
	}
	if recent[0].Deliveries != 1 {
		t.Fatalf("recent[0].Deliveries = %d, want 1", recent[0].Deliveries)
	}
}
