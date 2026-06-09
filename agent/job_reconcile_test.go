package agent

import (
	"os"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestReconcileOnRestoreFinalizesLostJob(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/sessions/S1/jobs", 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.Open(dir + "/sessions/S1/jobs.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1, 0).UTC()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_lost", Type: jobstore.JobShell, OwnerSessionID: "S1", VisibleToSession: "S1", StartedAt: &start}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var queued []jobNotification
	jm, err := newJobManager(dir, "S1", func(n jobNotification) { queued = append(queued, n) })
	if err != nil {
		t.Fatal(err)
	}
	jm.now = func() time.Time { return time.Unix(100, 0).UTC() }

	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost jobs: %v", err)
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs["job_lost"].Status != jobstore.StatusStopped || recs["job_lost"].Reason != "runtime_lost" {
		t.Fatalf("job_lost = %+v, want stopped/runtime_lost", recs["job_lost"])
	}
	if len(queued) != 1 || queued[0].JobID != "job_lost" {
		t.Fatalf("expected one queued runtime_lost notification, got %+v", queued)
	}
}
