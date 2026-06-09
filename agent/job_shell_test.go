package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
)

func newShellTestRig(t *testing.T) (*jobManager, execenv.StreamingExecutor) {
	t.Helper()
	jm := newTestJM(t)
	env := &execenv.LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	return jm, env
}

func waitForShellDone(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	jm.mu.Lock()
	run := jm.running[jobID]
	if run == nil {
		jm.mu.Unlock()
		return
	}
	done := run.done
	jm.mu.Unlock()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("job %s did not finish", jobID)
	}
}

func loadShellRecord(t *testing.T, jm *jobManager, jobID string) *jobstore.JobRecord {
	t.Helper()
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load jobs: %v", err)
	}
	rec := recs[jobID]
	if rec == nil {
		t.Fatalf("job %s not found", jobID)
	}
	return rec
}

func TestRunShellForegroundEphemeral(t *testing.T) {
	jm, se := newShellTestRig(t)
	res := runShell(context.Background(), jm, se, shellArgs{Command: "printf done", BlockTimeoutMS: 5000})
	if res.JobID != "" {
		t.Errorf("ephemeral job must have no job_id, got %q", res.JobID)
	}
	if res.Status != string(jobstore.StatusCompleted) || res.RunningInBackground {
		t.Errorf("res = %+v, want completed/foreground", res)
	}
	if len(jm.list(listFilter{})) != 0 {
		t.Errorf("ephemeral job must not appear in job_list")
	}
}

func TestRunShellPromotesOnTimeout(t *testing.T) {
	jm, se := newShellTestRig(t)
	res := runShell(context.Background(), jm, se, shellArgs{Command: "sleep 30", BlockTimeoutMS: 1000})
	if res.JobID == "" {
		t.Fatal("promoted job must have a job_id")
	}
	if res.Reason != "foreground_timeout" || !res.RunningInBackground || !res.TimedOut {
		t.Errorf("res = %+v, want foreground_timeout/background/timed_out", res)
	}
	if len(jm.list(listFilter{})) != 1 {
		t.Errorf("promoted job must appear in job_list")
	}
	_, _ = jm.stop(res.JobID)
	waitForShellDone(t, jm, res.JobID)
	rec := loadShellRecord(t, jm, res.JobID)
	if rec.Status != jobstore.StatusStopped || rec.Reason != "stopped" {
		t.Fatalf("stopped promoted job = %+v, want stopped/stopped", rec)
	}
}

func TestRunShellForegroundMaxRuntimeCreatesDurableStoppedJob(t *testing.T) {
	jm, se := newShellTestRig(t)
	start := time.Now()
	res := runShell(context.Background(), jm, se, shellArgs{
		Command:        "printf timeout-output; sleep 30",
		BlockTimeoutMS: 5000,
		MaxRuntimeMS:   500,
	})
	if time.Since(start) > 3*time.Second {
		t.Error("max runtime must return before block timeout")
	}
	if res.JobID == "" {
		t.Fatal("max runtime timeout must return a durable job_id")
	}
	if res.Status != string(jobstore.StatusStopped) || res.Reason != "run_timeout" || !res.TimedOut || res.RunningInBackground {
		t.Errorf("res = %+v, want stopped/run_timeout/timed_out/foreground", res)
	}

	jobs := jm.list(listFilter{})
	if len(jobs) != 1 {
		t.Fatalf("max runtime job must appear in job_list, got %+v", jobs)
	}
	if jobs[0].JobID != res.JobID || jobs[0].Status != jobstore.StatusStopped || jobs[0].Reason != "run_timeout" {
		t.Fatalf("job_list = %+v, want durable stopped/run_timeout job", jobs)
	}
	output, _, _, err := jm.readOutput(res.JobID, 1024)
	if err != nil {
		t.Fatalf("readOutput: %v", err)
	}
	if output != "timeout-output" {
		t.Fatalf("output = %q, want preserved runtime log", output)
	}
}

func TestRunShellBackgroundReturnsImmediately(t *testing.T) {
	jm, se := newShellTestRig(t)
	start := time.Now()
	res := runShell(context.Background(), jm, se, shellArgs{Command: "sleep 30", Background: true, BlockTimeoutMS: 120000})
	if time.Since(start) > 3*time.Second {
		t.Error("background must return promptly")
	}
	if res.JobID == "" || !res.RunningInBackground {
		t.Errorf("res = %+v", res)
	}
	_, _ = jm.stop(res.JobID)
	waitForShellDone(t, jm, res.JobID)
	rec := loadShellRecord(t, jm, res.JobID)
	if rec.Status != jobstore.StatusStopped || rec.Reason != "stopped" {
		t.Fatalf("stopped background job = %+v, want stopped/stopped", rec)
	}
}

func TestRunShellFinalizerRetriesAppendFailure(t *testing.T) {
	jm, se := newShellTestRig(t)
	appendErr := errors.New("temporary append failure")
	var failed atomic.Bool
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished && failed.CompareAndSwap(false, true) {
			return appendErr
		}
		return origAppend(e)
	}

	res := runShell(context.Background(), jm, se, shellArgs{Command: "printf retry", Background: true})
	if res.JobID == "" {
		t.Fatal("background shell must return a job_id")
	}
	waitForShellDone(t, jm, res.JobID)
	rec := loadShellRecord(t, jm, res.JobID)
	if rec.Status != jobstore.StatusCompleted || rec.Reason != "exit_zero" {
		t.Fatalf("record after retry = %+v, want completed/exit_zero", rec)
	}
	if !failed.Load() {
		t.Fatal("test did not exercise append failure")
	}
}
