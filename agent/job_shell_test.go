package agent

import (
	"context"
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

func TestRunShellForegroundEphemeral(t *testing.T) {
	jm, se := newShellTestRig(t)
	res := runShell(context.Background(), jm, se, shellArgs{Command: "printf done", BlockTimeoutMS: 5000})
	if res.JobID != "" {
		t.Errorf("ephemeral job must have no job_id, got %q", res.JobID)
	}
	if res.Status != jobstore.StatusCompleted || res.RunningInBackground {
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
}
