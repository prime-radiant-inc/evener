package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func newTestJM(t *testing.T) *jobManager {
	t.Helper()
	jm, err := newJobManager(t.TempDir(), "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	return jm
}

func TestJobManagerCreateAndList(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "make test", Description: "tests"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.JobID == "" || rec.Type != jobstore.JobShell || rec.Status != jobstore.StatusRunning {
		t.Fatalf("bad record: %+v", rec)
	}
	jobs := jm.list(listFilter{})
	if len(jobs) != 1 || jobs[0].JobID != rec.JobID {
		t.Fatalf("list = %+v", jobs)
	}
}

func TestJobManagerReadOutput(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.running[rec.JobID].output.Append([]byte("hello\n"))
	content, _, _, err := jm.readOutput(rec.JobID, 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if content != "hello\n" {
		t.Errorf("content = %q", content)
	}
}

func TestJobManagerFinalize(t *testing.T) {
	var queued []jobNotification
	jm, err := newJobManager(t.TempDir(), "S1", func(n jobNotification) {
		queued = append(queued, n)
	})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }

	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := jm.running[rec.JobID].done
	_, _ = jm.running[rec.JobID].output.Append([]byte("hello\n"))

	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil)

	select {
	case <-done:
	default:
		t.Fatal("done was not closed")
	}
	if _, ok := jm.running[rec.JobID]; ok {
		t.Fatal("job still running")
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := recs[rec.JobID]
	if got.Status != jobstore.StatusCompleted || got.Reason != "exit_zero" || got.OutputBytes != int64(len("hello\n")) {
		t.Fatalf("record = %+v", got)
	}
	if got.NotifyState != jobstore.NotifyPending {
		t.Fatalf("notify state = %q, want pending", got.NotifyState)
	}
	if len(queued) != 1 || queued[0].JobID != rec.JobID || queued[0].Status != string(jobstore.StatusCompleted) {
		t.Fatalf("queued = %+v", queued)
	}
}
