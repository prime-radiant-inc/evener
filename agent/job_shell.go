package agent

import (
	"context"
	"os"
	"path/filepath"
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
}

type shellOutputWriter struct {
	output *jobstore.OutputStore
}

func (w shellOutputWriter) Write(b []byte) (int, error) {
	return w.output.Append(b)
}

func runShell(ctx context.Context, jm *jobManager, se execenv.StreamingExecutor, args shellArgs) shellResult {
	blockTimeout := time.Duration(clampShellBlockTimeoutMS(args.BlockTimeoutMS)) * time.Millisecond

	run, err := jm.newDelayedShell(args)
	if err != nil {
		return shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}
	}

	handle, err := se.StreamCommand(ctx, args.Command, "", nil, shellOutputWriter{output: run.output})
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
		code, _ := handle.Wait()
		close(processDone)
		waitCh <- shellWaitResult{exitCode: code}
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
	select {
	case wait := <-waitCh:
		if runtimeTimedOut.Load() {
			return jm.finishForegroundRuntimeTimeout(run, wait)
		}
		status, reason, exitCode := jm.shellTerminal(run, wait.exitCode, runtimeTimedOut.Load())
		output, _, truncated, _ := tailOutput(run.output, shellInlineOutputBytes)
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
	}
}

func (jm *jobManager) finishForegroundRuntimeTimeout(run *runningJob, wait shellWaitResult) shellResult {
	status, reason, exitCode := jm.shellTerminal(run, wait.exitCode, true)
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
	status, reason, exitCode := jm.shellTerminal(run, wait.exitCode, runtimeTimedOut.Load())
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
		if err := jm.finalize(jobID, status, reason, exitCode); err == nil {
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

func (jm *jobManager) shellTerminal(run *runningJob, exitCode int, timedOut bool) (jobstore.Status, string, *int) {
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
	if exitCode == 0 {
		return jobstore.StatusCompleted, "exit_zero", &code
	}
	return jobstore.StatusFailed, "exit_nonzero", &code
}
