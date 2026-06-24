package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

func TestFinalizeDelegatePersistsSchemaValidationFailedReason(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	child.mu.Lock()
	child.comm.structured = map[string]any{"count": "not a number"}
	child.mu.Unlock()
	sub := completedDelegateSubagent(child, "validation failed")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJobWithID(parent.jobManager, child.ID(), "validate structured result", sub, jobstore.NewJobID(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "number"},
		},
		"required": []string{"count"},
	}, false)
	if err != nil {
		t.Fatalf("attachDelegateJobWithID: %v", err)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.StructuredResult != nil {
		t.Fatalf("structured_result persisted invalid value: %T", rec.StructuredResult)
	}
	if rec.StructuredResultValid == nil || *rec.StructuredResultValid {
		t.Fatalf("structured_result_valid = %v, want false", rec.StructuredResultValid)
	}
	if rec.StructuredResultReason != "schema_validation_failed" {
		t.Fatalf("structured_result_reason = %q, want schema_validation_failed", rec.StructuredResultReason)
	}
}

func TestCreateDelegateForegroundFinalizeFailureRetriesUntilDurable(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("finalize failure child complete")
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	finishAttempts := failAppendN(sess.jobManager, jobstore.EventJobFinished, 2)

	done := make(chan delegateResult, 1)
	go func() {
		done <- sess.createDelegate(context.Background(), delegateArgs{
			Task:           "finish while append fails",
			Background:     false,
			BlockTimeoutMS: 5000,
		})
	}()

	var res delegateResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("createDelegate did not retry finalization append failure")
	}
	if res.Err != nil {
		t.Fatalf("createDelegate returned error after retry: %v", res.Err)
	}
	if res.Status != jobstore.StatusCompleted {
		t.Fatalf("result = %+v, want completed", res)
	}
	if finishAttempts.Load() < 3 {
		t.Fatalf("job_finished attempts = %d, want retry until success", finishAttempts.Load())
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestFinalizeDelegateRetryAfterDurableFailureDoesNotDuplicateOutput(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		result:  "retry complete",
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	failAppendN(parent.jobManager, jobstore.EventJobFinished, 1)
	err = parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	if err != nil {
		t.Fatalf("finalizeDelegate after retry: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)

	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if got := strings.Count(output, "retry complete"); got != 1 {
		t.Fatalf("output contains delegate result %d times, want 1: %q", got, output)
	}
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record status = %q, want completed", rec.Status)
	}
}

func TestFinalizeDelegateRetriesJobFinishedAppendUntilDurable(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	child.mu.Lock()
	child.comm.structured = map[string]any{"summary": "retry structured"}
	child.mu.Unlock()
	sub := completedDelegateSubagent(child, "retry finished append")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	finishAttempts := failAppendN(parent.jobManager, jobstore.EventJobFinished, 2)

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	if finishAttempts.Load() < 3 {
		t.Fatalf("job_finished attempts = %d, want retry until success", finishAttempts.Load())
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record status = %q, want completed", rec.Status)
	}
	structured, ok := rec.StructuredResult.(map[string]any)
	if !ok || structured["summary"] != "retry structured" {
		t.Fatalf("structured_result = %+v, want retry structured", rec.StructuredResult)
	}
	if rec.StructuredResultValid == nil || !*rec.StructuredResultValid {
		t.Fatalf("structured_result_valid = %v, want true", rec.StructuredResultValid)
	}
}

func TestFinalizeDelegateRetriesOutputAppendWithoutClosingDone(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "retry output append")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry output", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	if err := run.output.Close(); err != nil {
		t.Fatalf("close delegate output: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	}()
	select {
	case err := <-done:
		t.Fatalf("finalizeDelegate returned before output was writable: %v", err)
	case <-run.done:
		t.Fatal("delegate done closed before output append was durable")
	case <-time.After(100 * time.Millisecond):
	}

	reopened, err := jobstore.OpenOutput(run.rec.OutputPath, 0)
	if err != nil {
		t.Fatalf("reopen delegate output: %v", err)
	}
	parent.jobManager.mu.Lock()
	run.output = reopened
	parent.jobManager.mu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("finalizeDelegate after output recovery: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finalizeDelegate did not retry after output recovery")
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
}

func TestFinalizeDelegateOutputPostWriteFailureDoesNotDuplicateOutput(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	prose := "post-write append failure"
	sub := completedDelegateSubagent(child, prose)
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry post-write output", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	metaTmpPath := run.rec.OutputPath + ".meta.json.tmp"
	if err := os.Mkdir(metaTmpPath, 0o755); err != nil {
		t.Fatalf("create metadata temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(metaTmpPath) })

	done := make(chan error, 1)
	go func() {
		done <- parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, readErr := os.ReadFile(run.rec.OutputPath)
		if readErr != nil {
			t.Fatalf("read output file: %v", readErr)
		}
		if strings.Contains(string(raw), prose) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delegate output was not written before append error")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("finalizeDelegate returned before metadata write recovered: %v", err)
	default:
	}

	if err := os.RemoveAll(metaTmpPath); err != nil {
		t.Fatalf("remove metadata temp directory: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("finalizeDelegate after metadata recovery: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finalizeDelegate did not retry after metadata recovery")
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read delegate output: %v", err)
	}
	if got := strings.Count(output, prose); got != 1 {
		t.Fatalf("delegate output contains terminal prose %d times, want 1: %q", got, output)
	}
}

func TestFinalizeDelegateRetriesNotificationPendingAppendKeepsTerminalResult(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "retry notification append")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry notification", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	pendingAttempts := failAppendN(parent.jobManager, jobstore.EventJobNotificationPending, 2)

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	if pendingAttempts.Load() < 3 {
		t.Fatalf("notification pending attempts = %d, want retry until success", pendingAttempts.Load())
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record status = %q, want completed", rec.Status)
	}
	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(output, "retry notification append") {
		t.Fatalf("output = %q, want retained terminal result", output)
	}
}

func TestFinalizeDelegateDuringManagerCloseDoesNotLeaveDoneOpen(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "close finalization")
	sub.running = true
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "close finalization", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	var signalOnce sync.Once
	parent.jobManager.mu.Lock()
	run.signal = func() {
		signalOnce.Do(func() {
			sub.mu.Lock()
			sub.running = false
			sub.status = SubagentCancelled
			sub.result = "closed during manager shutdown"
			done := sub.done
			sub.mu.Unlock()
			close(done)
		})
	}
	parent.jobManager.mu.Unlock()

	finalizeDone := make(chan error, 1)
	go func() {
		<-sub.done
		finalizeDone <- parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	}()

	closeDone := make(chan error, 1)
	start := time.Now()
	go func() {
		closeDone <- parent.jobManager.close()
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("jobManager.close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("jobManager.close waited for abandoned delegate timeout")
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("jobManager.close took %s, want no abandonment timeout", elapsed)
	}
	select {
	case err := <-finalizeDone:
		if err != nil && !errors.Is(err, errJobManagerClosing) {
			t.Fatalf("finalizeDelegate during close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("finalizeDelegate did not return during close")
	}
	select {
	case <-run.done:
	default:
		t.Fatal("delegate run.done left open after manager close")
	}
	parent.jobManager.mu.Lock()
	stuck := parent.jobManager.running[run.rec.JobID]
	parent.jobManager.mu.Unlock()
	if stuck != nil {
		t.Fatalf("delegate runtime still registered after manager close: %+v", stuck.rec)
	}
}

func TestFinalizeDelegateDuplicateTerminalNotificationsAreIdempotent(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "duplicate terminal")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "duplicate terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	var finished, pending atomic.Int32
	origAppend := parent.jobManager.appendEvent
	parent.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.JobID == run.rec.JobID {
			switch e.Kind {
			case jobstore.EventJobFinished:
				finished.Add(1)
			case jobstore.EventJobNotificationPending:
				pending.Add(1)
			}
		}
		return origAppend(e)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("first finalizeDelegate: %v", err)
	}
	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("second finalizeDelegate: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)

	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if got := strings.Count(output, "duplicate terminal"); got != 1 {
		t.Fatalf("output contains delegate result %d times, want 1: %q", got, output)
	}
	if finished.Load() != 1 || pending.Load() != 1 {
		t.Fatalf("terminal events finished=%d pending=%d, want one each", finished.Load(), pending.Load())
	}
}

func TestCreateDelegateForegroundOutputAppendFailureReturns(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				startedOnce.Do(func() { close(started) })
				<-release
				return communicateWithDefaultOutput("append failure child complete")
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	done := make(chan delegateResult, 1)
	go func() {
		done <- sess.createDelegate(context.Background(), delegateArgs{
			Task:           "finish while output append fails",
			Background:     false,
			BlockTimeoutMS: 5000,
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}
	run := waitForRunningDelegateJob(t, sess.jobManager)
	appendErr := run.output.Close()
	if appendErr != nil {
		t.Fatalf("close delegate output: %v", appendErr)
	}

	releaseOnce.Do(func() { close(release) })

	select {
	case res := <-done:
		t.Fatalf("createDelegate returned before output append recovered: %+v", res)
	case <-time.After(100 * time.Millisecond):
	}

	reopened, err := jobstore.OpenOutput(run.rec.OutputPath, 0)
	if err != nil {
		t.Fatalf("reopen delegate output: %v", err)
	}
	sess.jobManager.mu.Lock()
	run.output = reopened
	sess.jobManager.mu.Unlock()

	var res delegateResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("createDelegate did not retry after output append recovery")
	}
	if res.Err != nil {
		t.Fatalf("createDelegate returned error after output recovery: %v", res.Err)
	}
	if res.Status != jobstore.StatusCompleted {
		t.Fatalf("result = %+v, want completed", res)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
}
