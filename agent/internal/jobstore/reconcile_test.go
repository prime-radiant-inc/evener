package jobstore

import (
	"testing"
	"time"
)

func TestReconcileFinalizesLostRunningJob(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	recs := map[string]*JobRecord{
		"job_live": {JobID: "job_live", Status: StatusRunning, VisibleToSession: "S1"},
		"job_lost": {JobID: "job_lost", Status: StatusRunning, VisibleToSession: "S1"},
		"job_done": {JobID: "job_done", Status: StatusCompleted, VisibleToSession: "S1"},
	}
	live := map[string]bool{"job_live": true}

	events := Reconcile(recs, live, now)

	if len(events) != 1 {
		t.Fatalf("expected exactly 1 reconcile event, got %d: %+v", len(events), events)
	}
	e := events[0]
	if e.JobID != "job_lost" || e.Kind != EventJobFinished {
		t.Errorf("wrong job finalized: %+v", e)
	}
	if e.Status != StatusStopped || e.Reason != "runtime_lost" {
		t.Errorf("status/reason = %q/%q, want stopped/runtime_lost", e.Status, e.Reason)
	}
	if e.TerminalGen == "" {
		t.Errorf("reconcile event must carry a minted terminal_generation")
	}
	if e.EndedAt == nil || !e.EndedAt.Equal(now) {
		t.Errorf("ended_at = %v, want %v", e.EndedAt, now)
	}
}

func TestReconcileIsIdempotentOnSecondPass(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	recs := map[string]*JobRecord{
		"job_lost": {JobID: "job_lost", Status: StatusRunning, VisibleToSession: "S1"},
	}
	live := map[string]bool{}

	first := Reconcile(recs, live, now)
	// Apply the first pass, then re-fold and reconcile again: no new events.
	applyEvent(recs["job_lost"], first[0])
	second := Reconcile(recs, live, now)
	if len(second) != 0 {
		t.Errorf("second reconcile should be a no-op, got %+v", second)
	}
}
