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

func TestRestoreHistoricalWatchesEndedSeqAndTimestamp(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sid := "02wMz5TxvEMoJEDTDGOTi2"
	dir := jobsDir(stateDir, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) // w_late ends here
	t3 := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC) // w_early ends here (later!)
	t4 := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC) // runtime_lost

	// w_early registered first
	_ = store.Append(jobstore.Event{Kind: jobstore.EventWatchRegistered, TS: t0, WatchID: "w_early", Watch: &jobstore.WatchEvent{Generation: "g1", OwnerSessionID: sid, VisibleSessionID: sid, Target: "job_a", ConfigHash: "cfgA"}})
	// w_late registered second
	_ = store.Append(jobstore.Event{Kind: jobstore.EventWatchRegistered, TS: t1, WatchID: "w_late", Watch: &jobstore.WatchEvent{Generation: "g2", OwnerSessionID: sid, VisibleSessionID: sid, Target: "job_b", ConfigHash: "cfgB"}})
	// w_lost registered
	_ = store.Append(jobstore.Event{Kind: jobstore.EventWatchRegistered, TS: t1, WatchID: "w_lost", Watch: &jobstore.WatchEvent{Generation: "g3", OwnerSessionID: sid, VisibleSessionID: sid, Target: "job_c", ConfigHash: "cfgC"}})

	// w_late cleared first
	_ = store.Append(jobstore.Event{Kind: jobstore.EventWatchCleared, TS: t2, WatchID: "w_late", Watch: &jobstore.WatchEvent{Generation: "g2", EndReason: "cleared"}})
	// w_early cleared second (ends later)
	_ = store.Append(jobstore.Event{Kind: jobstore.EventWatchCleared, TS: t3, WatchID: "w_early", Watch: &jobstore.WatchEvent{Generation: "g1", EndReason: "completed"}})
	// w_lost cleared with runtime_lost
	_ = store.Append(jobstore.Event{Kind: jobstore.EventWatchCleared, TS: t4, WatchID: "w_lost", Watch: &jobstore.WatchEvent{Generation: "g3", EndReason: "runtime_lost"}})
	_ = store.Close()

	// Reopen job manager from disk
	jm, err := newJobManager(stateDir, sid, func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	defer jm.closeRuntimeState(t.Context())

	recent := jm.recentWatchSummaries()
	// w_lost should NOT be in the history ring; only w_early and w_late
	if len(recent) != 2 {
		t.Fatalf("recentWatchSummaries length = %d, want 2 (runtime_lost excluded): %+v", len(recent), recent)
	}
	// recent is latest-first: w_early ended at t3, w_late ended at t2
	if recent[0].ID != "w_early" || recent[1].ID != "w_late" {
		t.Fatalf("recent watches ordering = [%s, %s], want [w_early, w_late]", recent[0].ID, recent[1].ID)
	}
	// Timestamps must match the durable event TS, not restart time
	wantT3 := t3.Format(time.RFC3339Nano)
	wantT2 := t2.Format(time.RFC3339Nano)
	if recent[0].EndedAt != wantT3 {
		t.Fatalf("recent[0].EndedAt = %q, want %q", recent[0].EndedAt, wantT3)
	}
	if recent[1].EndedAt != wantT2 {
		t.Fatalf("recent[1].EndedAt = %q, want %q", recent[1].EndedAt, wantT2)
	}

	// Inspect w_early retains exact EndedAt timestamp
	inspected := jm.inspectWatchByID("w_early")
	if inspected.EndedAt != wantT3 {
		t.Fatalf("inspected.EndedAt = %q, want %q", inspected.EndedAt, wantT3)
	}

	// runtime_lost watch does not enter history
	if lost := jm.inspectWatchByID("w_lost"); lost.Watching || lost.Source != "" || lost.EndReason != "" {
		t.Fatalf("runtime_lost watch entered history: %+v", lost)
	}
}
