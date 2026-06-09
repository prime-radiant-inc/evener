package jobstore

import (
	"testing"
	"time"
)

func TestReconcileFinalizesLostRunningJobs(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	recs := map[string]*JobRecord{
		"job_live":   {JobID: "job_live", Status: StatusRunning, VisibleToSession: "S1"},
		"job_lost_b": {JobID: "job_lost_b", Status: StatusRunning, VisibleToSession: "S1"},
		"job_lost_a": {JobID: "job_lost_a", Status: StatusRunning, VisibleToSession: "S1"},
		"job_done":   {JobID: "job_done", Status: StatusCompleted, VisibleToSession: "S1"},
	}
	live := map[string]bool{"job_live": true}

	events := Reconcile(recs, live, now)

	wantJobIDs := []string{"job_lost_a", "job_lost_b"}
	if len(events) != len(wantJobIDs) {
		t.Fatalf("expected %d reconcile events, got %d: %+v", len(wantJobIDs), len(events), events)
	}
	for i, wantJobID := range wantJobIDs {
		e := events[i]
		if e.JobID != wantJobID || e.Kind != EventJobFinished {
			t.Errorf("event[%d] finalized wrong job: %+v", i, e)
		}
		if e.Status != StatusStopped || e.Reason != "runtime_lost" {
			t.Errorf("event[%d] status/reason = %q/%q, want stopped/runtime_lost", i, e.Status, e.Reason)
		}
		if e.TerminalGen == "" {
			t.Errorf("event[%d] must carry a minted terminal_generation", i)
		}
		if e.EndedAt == nil || !e.EndedAt.Equal(now) {
			t.Errorf("event[%d] ended_at = %v, want %v", i, e.EndedAt, now)
		}
	}
}

func TestReconcileIsIdempotentOnSecondPass(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	recs := map[string]*JobRecord{
		"job_lost": {JobID: "job_lost", Status: StatusRunning, VisibleToSession: "S1"},
	}
	live := map[string]bool{}

	first := Reconcile(recs, live, now)
	if len(first) != 1 {
		t.Fatalf("expected first reconcile to return 1 event, got %d: %+v", len(first), first)
	}
	// Apply the first pass, then re-fold and reconcile again: no new events.
	applyEvent(recs["job_lost"], first[0])
	second := Reconcile(recs, live, now)
	if len(second) != 0 {
		t.Errorf("second reconcile should be a no-op, got %+v", second)
	}
}
