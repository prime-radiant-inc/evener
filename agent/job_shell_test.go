package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
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

func TestRunShellForegroundEphemeralReturnsFullOutput(t *testing.T) {
	jm, se := newShellTestRig(t)
	res := runShell(context.Background(), jm, se, shellArgs{
		Command:        "head -c 70000 </dev/zero | tr '\\0' 'x'",
		BlockTimeoutMS: 5000,
	})
	if res.Status != string(jobstore.StatusCompleted) || res.RunningInBackground {
		t.Fatalf("res = %+v, want completed/foreground", res)
	}
	if got := len(res.Output); got != 70000 {
		t.Fatalf("output len = %d, want full 70000-byte foreground output", got)
	}
	if res.Truncated {
		t.Fatalf("truncated = true, want false for full foreground output")
	}
	if len(jm.list(listFilter{})) != 0 {
		t.Errorf("ephemeral job must not appear in job_list")
	}
}

type waitErrorStreamingExecutor struct{}

func (waitErrorStreamingExecutor) StreamCommand(_ context.Context, _ string, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	_, _ = out.Write([]byte("partial"))
	return &execenv.StreamHandle{
		Wait:   func() (int, error) { return 0, errors.New("wait failed") },
		Signal: func() {},
	}, nil
}

type blockingStartStreamingExecutor struct {
	started  chan struct{}
	release  chan struct{}
	waitDone chan struct{}
	once     sync.Once
	signals  atomic.Int32
}

func newBlockingStartStreamingExecutor() *blockingStartStreamingExecutor {
	return &blockingStartStreamingExecutor{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		waitDone: make(chan struct{}),
	}
}

func (e *blockingStartStreamingExecutor) StreamCommand(_ context.Context, _ string, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	_, _ = out.Write([]byte("partial"))
	close(e.started)
	<-e.release
	return &execenv.StreamHandle{
		Wait: func() (int, error) {
			<-e.waitDone
			return -1, nil
		},
		Signal: func() {
			e.signals.Add(1)
			e.once.Do(func() { close(e.waitDone) })
		},
	}, nil
}

type delayedExitStreamingExecutor struct {
	release chan struct{}
	signals atomic.Int32
}

func newDelayedExitStreamingExecutor() *delayedExitStreamingExecutor {
	return &delayedExitStreamingExecutor{release: make(chan struct{})}
}

func (e *delayedExitStreamingExecutor) StreamCommand(_ context.Context, _ string, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	_, _ = out.Write([]byte("running"))
	return &execenv.StreamHandle{
		Wait: func() (int, error) {
			<-e.release
			return 143, nil
		},
		Signal: func() {
			e.signals.Add(1)
		},
	}, nil
}

func TestRunShellForegroundWaitErrorFailsJob(t *testing.T) {
	jm := newTestJM(t)
	res := runShell(context.Background(), jm, waitErrorStreamingExecutor{}, shellArgs{Command: "x", BlockTimeoutMS: 5000})
	if res.Status != string(jobstore.StatusFailed) || res.Reason != "wait_failed" || res.RunningInBackground {
		t.Fatalf("res = %+v, want failed/wait_failed foreground", res)
	}
	if res.Output != "partial" {
		t.Fatalf("output = %q, want partial", res.Output)
	}
}

func TestRunShellBackgroundWaitErrorFinalizesFailed(t *testing.T) {
	jm := newTestJM(t)
	res := runShell(context.Background(), jm, waitErrorStreamingExecutor{}, shellArgs{Command: "x", Background: true})
	if res.JobID == "" || !res.RunningInBackground {
		t.Fatalf("res = %+v, want background job", res)
	}
	waitForShellDone(t, jm, res.JobID)
	rec := loadShellRecord(t, jm, res.JobID)
	if rec.Status != jobstore.StatusFailed || rec.Reason != "wait_failed" {
		t.Fatalf("record = %+v, want failed/wait_failed", rec)
	}
}

func TestRunShellBackgroundCloseDuringStartDoesNotCommitJob(t *testing.T) {
	jm := newTestJM(t)
	se := newBlockingStartStreamingExecutor()

	resultCh := make(chan shellResult, 1)
	go func() {
		resultCh <- runShell(context.Background(), jm, se, shellArgs{Command: "sleep 30", Background: true})
	}()

	select {
	case <-se.started:
	case <-time.After(2 * time.Second):
		t.Fatal("streaming executor did not start")
	}

	closeCh := make(chan error, 1)
	go func() {
		closeCh <- jm.close()
	}()
	waitForJobManagerClosing(t, jm)
	close(se.release)

	var res shellResult
	select {
	case res = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runShell did not return after job manager close")
	}
	if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" || res.JobID != "" || res.RunningInBackground {
		t.Fatalf("res = %+v, want failed/start_failed with no durable job", res)
	}
	if se.signals.Load() == 0 {
		t.Fatal("late-installed shell signal was not invoked after close requested stop")
	}

	select {
	case err := <-closeCh:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job manager close did not finish")
	}

	store, err := jobstore.Open(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("reopen job store: %v", err)
	}
	defer store.Close()
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("load reopened store: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("background job committed during close: %+v", recs)
	}
}

func TestCommitDelayedShellAfterCloseAbandonmentFails(t *testing.T) {
	jm := newTestJM(t)
	run, err := jm.newDelayedShell(shellArgs{Command: "sleep 30", Background: true})
	if err != nil {
		t.Fatalf("newDelayedShell: %v", err)
	}
	jm.mu.Lock()
	jm.closing = true
	delete(jm.running, run.rec.JobID)
	jm.mu.Unlock()
	defer func() {
		_ = run.output.Close()
		_ = os.Remove(run.rec.OutputPath)
		close(run.done)
	}()

	if err := jm.commitDelayedShell(run); !errors.Is(err, errJobManagerClosing) {
		t.Fatalf("commitDelayedShell error = %v, want %v", err, errJobManagerClosing)
	}
}

func waitForJobManagerClosing(t *testing.T, jm *jobManager) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		jm.mu.Lock()
		closing := jm.closing
		jm.mu.Unlock()
		if closing {
			return
		}
		select {
		case <-deadline:
			t.Fatal("job manager close did not set closing")
		case <-tick.C:
		}
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
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("stopped promoted job = %+v, want cancelled/stopped_by_parent", rec)
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
	if res.Status != string(jobstore.StatusStopped) || res.Reason != "run_timeout" || res.TimedOut || res.RunningInBackground {
		t.Errorf("res = %+v, want stopped/run_timeout/not_timed_out/foreground", res)
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

func TestRunShellForegroundMaxRuntimeFinalizerFailureConvergesDetached(t *testing.T) {
	jm, se := newShellTestRig(t)
	appendErr := errors.New("temporary append failure")
	var finishAttempts atomic.Int32
	var pendingAttempts atomic.Int32
	failuresBeforeSuccess := int32(shellFinalizeAttempts + 2)
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		switch e.Kind {
		case jobstore.EventJobFinished:
			if finishAttempts.Add(1) <= failuresBeforeSuccess {
				return appendErr
			}
		case jobstore.EventJobNotificationPending:
			if pendingAttempts.Add(1) <= failuresBeforeSuccess {
				return appendErr
			}
		}
		return origAppend(e)
	}

	res := runShell(context.Background(), jm, se, shellArgs{
		Command:        "printf timeout-retry; sleep 30",
		BlockTimeoutMS: 5000,
		MaxRuntimeMS:   100,
	})
	if res.JobID == "" {
		t.Fatal("max runtime finalize failure must still return the durable job_id")
	}
	if res.Status != string(jobstore.StatusFailed) || res.Reason != "finalize_failed" || res.TimedOut || !res.RunningInBackground {
		t.Fatalf("res = %+v, want failed/finalize_failed/not_timed_out/background", res)
	}
	if finishAttempts.Load() < shellFinalizeAttempts {
		t.Fatalf("finish attempts = %d, want bounded attempts exhausted", finishAttempts.Load())
	}

	waitForShellDone(t, jm, res.JobID)
	rec := loadShellRecord(t, jm, res.JobID)
	if rec.Status != jobstore.StatusStopped || rec.Reason != "run_timeout" {
		t.Fatalf("record after detached convergence = %+v, want stopped/run_timeout", rec)
	}
	if pendingAttempts.Load() <= failuresBeforeSuccess {
		t.Fatalf("pending attempts = %d, want detached notification retries past %d", pendingAttempts.Load(), failuresBeforeSuccess)
	}
	output, _, _, err := jm.readOutput(res.JobID, 1024)
	if err != nil {
		t.Fatalf("readOutput: %v", err)
	}
	if output != "timeout-retry" {
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
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("stopped background job = %+v, want cancelled/stopped_by_parent", rec)
	}
}

func TestRunShellBackgroundStopWinsOverLaterRuntimeTimeout(t *testing.T) {
	jm := newTestJM(t)
	se := newDelayedExitStreamingExecutor()
	res := runShell(context.Background(), jm, se, shellArgs{
		Command:      "sleep 30",
		Background:   true,
		MaxRuntimeMS: 50,
	})
	if res.JobID == "" || !res.RunningInBackground {
		t.Fatalf("res = %+v, want background job", res)
	}

	if _, err := jm.stop(res.JobID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	close(se.release)

	waitForShellDone(t, jm, res.JobID)
	rec := loadShellRecord(t, jm, res.JobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent despite later runtime timeout", rec)
	}
	if se.signals.Load() < 2 {
		t.Fatalf("signals = %d, want stop signal and later runtime-timeout signal", se.signals.Load())
	}
}

func TestRunShellBackgroundSurvivesToolContextCancellation(t *testing.T) {
	jm, se := newShellTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	res := runShell(ctx, jm, se, shellArgs{Command: "sleep 1; printf survived", Background: true})
	cancel()
	if res.JobID == "" || !res.RunningInBackground {
		t.Fatalf("res = %+v, want background job", res)
	}

	waitForShellDone(t, jm, res.JobID)
	rec := loadShellRecord(t, jm, res.JobID)
	if rec.Status != jobstore.StatusCompleted || rec.Reason != "exit_zero" {
		t.Fatalf("record = %+v, want completed/exit_zero", rec)
	}
	output, _, _, err := jm.readOutput(res.JobID, 1024)
	if err != nil {
		t.Fatalf("readOutput: %v", err)
	}
	if output != "survived" {
		t.Fatalf("output = %q, want survived", output)
	}
}

func TestRunShellForegroundCancelsBeforePromotion(t *testing.T) {
	jm, se := newShellTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	res := runShell(ctx, jm, se, shellArgs{Command: "sleep 30", BlockTimeoutMS: 5000})
	if res.Status != string(jobstore.StatusStopped) || res.Reason != "cancelled" || res.RunningInBackground {
		t.Fatalf("res = %+v, want stopped/cancelled foreground", res)
	}
	if jobs := jm.list(listFilter{}); len(jobs) != 0 {
		t.Fatalf("cancelled foreground job must not be durable, got %+v", jobs)
	}
}

func TestStartOnlyContextSeesPreCanceledParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	startCtx, detach := newStartOnlyContext(parent)
	detach()

	select {
	case <-startCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("start-only context did not observe pre-canceled parent")
	}
	if startCtx.Err() == nil {
		t.Fatal("start-only context Err() = nil, want cancellation")
	}
}

func TestStartOnlyContextIgnoresCancellationAfterDetach(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	startCtx, detach := newStartOnlyContext(parent)
	detach()
	cancel()

	select {
	case <-startCtx.Done():
		t.Fatal("start-only context cancelled after detach")
	case <-time.After(100 * time.Millisecond):
	}
	if startCtx.Err() != nil {
		t.Fatalf("start-only context Err() = %v, want nil", startCtx.Err())
	}
}

func TestRunShellDetachedFinalizerRetriesUntilDurable(t *testing.T) {
	jm, se := newShellTestRig(t)
	appendErr := errors.New("temporary append failure")
	var finishAttempts atomic.Int32
	var pendingAttempts atomic.Int32
	failuresBeforeSuccess := int32(shellFinalizeAttempts + 2)
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		switch e.Kind {
		case jobstore.EventJobFinished:
			if finishAttempts.Add(1) <= failuresBeforeSuccess {
				return appendErr
			}
		case jobstore.EventJobNotificationPending:
			if pendingAttempts.Add(1) <= failuresBeforeSuccess {
				return appendErr
			}
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
	if finishAttempts.Load() <= failuresBeforeSuccess || pendingAttempts.Load() <= failuresBeforeSuccess {
		t.Fatalf("attempts finish=%d pending=%d, want retries past %d", finishAttempts.Load(), pendingAttempts.Load(), failuresBeforeSuccess)
	}
}
