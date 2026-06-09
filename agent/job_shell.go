package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
)

const (
	defaultShellBlockTimeoutMS = 120000
	minShellBlockTimeoutMS     = 1000
	maxShellBlockTimeoutMS     = 600000
	shellInlineOutputBytes     = 64 * 1024
	shellFinalizeAttempts      = 5
	shellFinalizeRetryDelay    = 20 * time.Millisecond
	shellFinalizeMaxRetryDelay = 50 * time.Millisecond
)

type shellArgs struct {
	Command        string
	Description    string
	Background     bool
	BlockTimeoutMS int
	MaxRuntimeMS   int
}

type shellResult struct {
	JobID               string
	Type                string
	Status              string
	Reason              string
	RunningInBackground bool
	TimedOut            bool
	ExitCode            *int
	Output              string
	Truncated           bool
}

type shellWaitResult struct {
	exitCode int
	err      error
}

type shellOutputWriter struct {
	output *jobstore.OutputStore
}

func (w shellOutputWriter) Write(b []byte) (int, error) {
	return w.output.Append(b)
}

func runShell(ctx context.Context, jm *jobManager, se execenv.StreamingExecutor, args shellArgs) shellResult {
	blockTimeout := time.Duration(clampShellBlockTimeoutMS(args.BlockTimeoutMS)) * time.Millisecond
	if ctx != nil {
		select {
		case <-ctx.Done():
			return shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}
		default:
		}
	}

	run, err := jm.newDelayedShell(args)
	if err != nil {
		return shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}
	}

	startCtx, detachStartCtx := newStartOnlyContext(ctx)
	handle, err := se.StreamCommand(startCtx, args.Command, "", nil, shellOutputWriter{output: run.output})
	detachStartCtx()
	if err != nil {
		jm.discardDelayedShell(run)
		return shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}
	}

	jm.setShellSignal(run, handle.Signal)
	waitCh := make(chan shellWaitResult, 1)
	processDone := make(chan struct{})
	var runtimeTimeoutCh <-chan struct{}
	var runtimeTimedOut atomic.Bool

	go func() {
		code, err := handle.Wait()
		close(processDone)
		waitCh <- shellWaitResult{exitCode: code, err: err}
	}()

	if args.MaxRuntimeMS > 0 {
		timeoutCh := make(chan struct{})
		runtimeTimeoutCh = timeoutCh
		timer := time.NewTimer(time.Duration(args.MaxRuntimeMS) * time.Millisecond)
		go func() {
			defer timer.Stop()
			select {
			case <-timer.C:
				runtimeTimedOut.Store(true)
				handle.Signal()
				close(timeoutCh)
			case <-processDone:
			}
		}()
	}

	if args.Background {
		if err := jm.commitDelayedShell(run); err != nil {
			handle.Signal()
			jm.discardDelayedShell(run)
			return shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}
		}
		go jm.finalizeShellWhenDone(run, waitCh, &runtimeTimedOut)
		return shellResult{
			JobID:               run.rec.JobID,
			Type:                string(run.rec.Type),
			Status:              string(jobstore.StatusRunning),
			RunningInBackground: true,
		}
	}

	timer := time.NewTimer(blockTimeout)
	defer timer.Stop()
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}
	select {
	case wait := <-waitCh:
		if runtimeTimedOut.Load() {
			return jm.finishForegroundRuntimeTimeout(run, wait)
		}
		status, reason, exitCode := jm.shellTerminal(run, wait.exitCode, runtimeTimedOut.Load(), wait.err)
		output, _, truncated, _ := fullOutput(run.output)
		jm.discardDelayedShell(run)
		return shellResult{
			Type:                string(run.rec.Type),
			Status:              string(status),
			Reason:              reason,
			RunningInBackground: false,
			TimedOut:            runtimeTimedOut.Load(),
			ExitCode:            exitCode,
			Output:              output,
			Truncated:           truncated,
		}
	case <-runtimeTimeoutCh:
		wait := <-waitCh
		return jm.finishForegroundRuntimeTimeout(run, wait)
	case <-timer.C:
		if runtimeTimedOut.Load() {
			wait := <-waitCh
			return jm.finishForegroundRuntimeTimeout(run, wait)
		}
		if err := jm.commitDelayedShell(run); err != nil {
			handle.Signal()
			jm.discardDelayedShell(run)
			return shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}
		}
		output, _, truncated, _ := tailOutput(run.output, shellInlineOutputBytes)
		go jm.finalizeShellWhenDone(run, waitCh, &runtimeTimedOut)
		return shellResult{
			JobID:               run.rec.JobID,
			Type:                string(run.rec.Type),
			Status:              string(jobstore.StatusRunning),
			Reason:              "foreground_timeout",
			RunningInBackground: true,
			TimedOut:            true,
			Output:              output,
			Truncated:           truncated,
		}
	case <-ctxDone:
		handle.Signal()
		wait := <-waitCh
		output, _, truncated, _ := fullOutput(run.output)
		jm.discardDelayedShell(run)
		return shellResult{
			Type:                string(run.rec.Type),
			Status:              string(jobstore.StatusStopped),
			Reason:              "cancelled",
			RunningInBackground: false,
			TimedOut:            false,
			ExitCode:            &wait.exitCode,
			Output:              output,
			Truncated:           truncated,
		}
	}
}

func fullOutput(output *jobstore.OutputStore) (string, int64, bool, error) {
	_, total, _, err := output.Tail(0)
	if err != nil {
		return "", total, false, err
	}
	if total == 0 {
		return "", 0, false, nil
	}
	return tailOutput(output, int(total))
}

type startOnlyContext struct {
	parent     context.Context
	done       chan struct{}
	detach     chan struct{}
	cancelOnce sync.Once
	detachOnce sync.Once
	mu         sync.Mutex
	err        error
	detached   bool
}

func newStartOnlyContext(parent context.Context) (*startOnlyContext, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx := &startOnlyContext{
		parent: parent,
		done:   make(chan struct{}),
		detach: make(chan struct{}),
	}
	if err := parent.Err(); err != nil {
		ctx.cancel(err)
		return ctx, ctx.detachStart
	}
	go func() {
		select {
		case <-parent.Done():
			ctx.cancel(parent.Err())
		case <-ctx.detach:
		}
	}()
	return ctx, ctx.detachStart
}

func (c *startOnlyContext) Deadline() (time.Time, bool) {
	return c.parent.Deadline()
}

func (c *startOnlyContext) Done() <-chan struct{} {
	return c.done
}

func (c *startOnlyContext) Err() error {
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	default:
		return nil
	}
}

func (c *startOnlyContext) Value(key any) any {
	return c.parent.Value(key)
}

func (c *startOnlyContext) DetachAfterStart() {
	c.detachStart()
}

func (c *startOnlyContext) cancel(err error) {
	if err == nil {
		err = context.Canceled
	}
	c.cancelOnce.Do(func() {
		c.mu.Lock()
		if c.detached {
			c.mu.Unlock()
			return
		}
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *startOnlyContext) detachStart() {
	c.detachOnce.Do(func() {
		c.mu.Lock()
		c.detached = true
		c.mu.Unlock()
		close(c.detach)
	})
}

func (jm *jobManager) finishForegroundRuntimeTimeout(run *runningJob, wait shellWaitResult) shellResult {
	status, reason, exitCode := jm.shellTerminal(run, wait.exitCode, true, wait.err)
	if err := jm.commitDelayedShell(run); err != nil {
		jm.discardDelayedShell(run)
		return shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}
	}
	output, _, truncated, _ := tailOutput(run.output, shellInlineOutputBytes)
	if err := jm.finalizeShellWithRetry(run.rec.JobID, status, reason, exitCode); err != nil {
		go jm.finalizeShellUntilDurable(run.rec.JobID, status, reason, exitCode)
		return shellResult{
			JobID:               run.rec.JobID,
			Type:                string(run.rec.Type),
			Status:              string(jobstore.StatusFailed),
			Reason:              "finalize_failed",
			RunningInBackground: true,
			TimedOut:            false,
			ExitCode:            exitCode,
			Output:              output,
			Truncated:           truncated,
		}
	}
	return shellResult{
		JobID:               run.rec.JobID,
		Type:                string(run.rec.Type),
		Status:              string(status),
		Reason:              reason,
		RunningInBackground: false,
		TimedOut:            false,
		ExitCode:            exitCode,
		Output:              output,
		Truncated:           truncated,
	}
}

func clampShellBlockTimeoutMS(timeoutMS int) int {
	if timeoutMS == 0 {
		return defaultShellBlockTimeoutMS
	}
	if timeoutMS < minShellBlockTimeoutMS {
		return minShellBlockTimeoutMS
	}
	if timeoutMS > maxShellBlockTimeoutMS {
		return maxShellBlockTimeoutMS
	}
	return timeoutMS
}

func (jm *jobManager) newDelayedShell(args shellArgs) (*runningJob, error) {
	startedAt := jm.now()
	jobID := jobstore.NewJobID()
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	output, err := jobstore.OpenOutput(outputPath, 0)
	if err != nil {
		return nil, err
	}
	run := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:            jobID,
			Type:             jobstore.JobShell,
			Status:           jobstore.StatusRunning,
			Command:          args.Command,
			Description:      args.Description,
			OwnerSessionID:   jm.sessionID,
			VisibleToSession: jm.sessionID,
			StartedAt:        startedAt,
			OutputPath:       outputPath,
		},
		output: output,
		signal: func() {},
		done:   make(chan struct{}),
	}

	jm.mu.Lock()
	jm.running[jobID] = run
	jm.mu.Unlock()
	return run, nil
}

func (jm *jobManager) setShellSignal(run *runningJob, signal func()) {
	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run {
		run.signal = signal
	}
	jm.mu.Unlock()
}

func (jm *jobManager) commitDelayedShell(run *runningJob) error {
	jm.mu.Lock()
	if jm.running[run.rec.JobID] != run {
		jm.mu.Unlock()
		return nil
	}
	if run.durableStarted {
		jm.mu.Unlock()
		return nil
	}
	rec := cloneJobRecord(run.rec)
	jm.mu.Unlock()

	startedAt := rec.StartedAt
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            rec.JobID,
		Type:             rec.Type,
		Command:          rec.Command,
		Description:      rec.Description,
		OwnerSessionID:   rec.OwnerSessionID,
		VisibleToSession: rec.VisibleToSession,
		StartedAt:        &startedAt,
	}); err != nil {
		return err
	}

	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run {
		run.durableStarted = true
	}
	jm.mu.Unlock()
	return nil
}

func (jm *jobManager) discardDelayedShell(run *runningJob) {
	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run {
		delete(jm.running, run.rec.JobID)
	}
	jm.mu.Unlock()

	_ = run.output.Close()
	_ = os.Remove(run.rec.OutputPath)
	close(run.done)
}

func (jm *jobManager) finalizeShellWhenDone(run *runningJob, waitCh <-chan shellWaitResult, runtimeTimedOut *atomic.Bool) {
	wait := <-waitCh
	status, reason, exitCode := jm.shellTerminal(run, wait.exitCode, runtimeTimedOut.Load(), wait.err)
	jm.finalizeShellUntilDurable(run.rec.JobID, status, reason, exitCode)
}

func (jm *jobManager) finalizeShellWithRetry(jobID string, status jobstore.Status, reason string, exitCode *int) error {
	var err error
	for attempt := 0; attempt < shellFinalizeAttempts; attempt++ {
		err = jm.finalize(jobID, status, reason, exitCode)
		if err == nil {
			return nil
		}
		if attempt+1 < shellFinalizeAttempts {
			time.Sleep(shellFinalizeBackoff(attempt))
		}
	}
	return err
}

func (jm *jobManager) finalizeShellUntilDurable(jobID string, status jobstore.Status, reason string, exitCode *int) {
	attempt := 0
	for {
		if err := jm.finalize(jobID, status, reason, exitCode); err == nil || errors.Is(err, jobstore.ErrStoreClosed) {
			return
		}
		time.Sleep(shellFinalizeBackoff(attempt))
		attempt++
	}
}

func shellFinalizeBackoff(attempt int) time.Duration {
	delay := shellFinalizeRetryDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= shellFinalizeMaxRetryDelay {
			return shellFinalizeMaxRetryDelay
		}
	}
	return delay
}

func (jm *jobManager) shellTerminal(run *runningJob, exitCode int, timedOut bool, waitErr error) (jobstore.Status, string, *int) {
	code := exitCode
	if timedOut {
		return jobstore.StatusStopped, "run_timeout", &code
	}
	jm.mu.Lock()
	stopStatus, stopReason := run.stopStatus, run.stopReason
	jm.mu.Unlock()
	if stopStatus != "" {
		return stopStatus, stopReason, &code
	}
	if waitErr != nil {
		return jobstore.StatusFailed, "wait_failed", &code
	}
	if exitCode == 0 {
		return jobstore.StatusCompleted, "exit_zero", &code
	}
	return jobstore.StatusFailed, "exit_nonzero", &code
}
