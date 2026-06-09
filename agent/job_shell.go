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
	Status              jobstore.Status
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
		return shellResult{Type: string(jobstore.JobShell), Status: jobstore.StatusFailed, Reason: "start_failed"}
	}

	handle, err := se.StreamCommand(ctx, args.Command, "", nil, shellOutputWriter{output: run.output})
	if err != nil {
		jm.discardDelayedShell(run)
		return shellResult{Type: string(jobstore.JobShell), Status: jobstore.StatusFailed, Reason: "start_failed"}
	}

	jm.setShellSignal(run, handle.Signal)
	waitCh := make(chan shellWaitResult, 1)
	processDone := make(chan struct{})
	var runtimeTimedOut atomic.Bool

	go func() {
		code, _ := handle.Wait()
		close(processDone)
		waitCh <- shellWaitResult{exitCode: code}
	}()

	if args.MaxRuntimeMS > 0 {
		timer := time.NewTimer(time.Duration(args.MaxRuntimeMS) * time.Millisecond)
		go func() {
			defer timer.Stop()
			select {
			case <-timer.C:
				runtimeTimedOut.Store(true)
				handle.Signal()
			case <-processDone:
			}
		}()
	}

	if args.Background {
		if err := jm.commitDelayedShell(run); err != nil {
			handle.Signal()
			jm.discardDelayedShell(run)
			return shellResult{Type: string(jobstore.JobShell), Status: jobstore.StatusFailed, Reason: "start_failed"}
		}
		go jm.finalizeShellWhenDone(run.rec.JobID, waitCh, &runtimeTimedOut)
		return shellResult{
			JobID:               run.rec.JobID,
			Type:                string(run.rec.Type),
			Status:              jobstore.StatusRunning,
			RunningInBackground: true,
		}
	}

	timer := time.NewTimer(blockTimeout)
	defer timer.Stop()
	select {
	case wait := <-waitCh:
		status, reason, exitCode := shellTerminal(wait.exitCode, runtimeTimedOut.Load())
		output, _, truncated, _ := tailOutput(run.output, shellInlineOutputBytes)
		jm.discardDelayedShell(run)
		return shellResult{
			Type:                string(run.rec.Type),
			Status:              status,
			Reason:              reason,
			RunningInBackground: false,
			TimedOut:            runtimeTimedOut.Load(),
			ExitCode:            exitCode,
			Output:              output,
			Truncated:           truncated,
		}
	case <-timer.C:
		if err := jm.commitDelayedShell(run); err != nil {
			handle.Signal()
			jm.discardDelayedShell(run)
			return shellResult{Type: string(jobstore.JobShell), Status: jobstore.StatusFailed, Reason: "start_failed"}
		}
		output, _, truncated, _ := tailOutput(run.output, shellInlineOutputBytes)
		go jm.finalizeShellWhenDone(run.rec.JobID, waitCh, &runtimeTimedOut)
		return shellResult{
			JobID:               run.rec.JobID,
			Type:                string(run.rec.Type),
			Status:              jobstore.StatusRunning,
			Reason:              "foreground_timeout",
			RunningInBackground: true,
			TimedOut:            true,
			Output:              output,
			Truncated:           truncated,
		}
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

func (jm *jobManager) finalizeShellWhenDone(jobID string, waitCh <-chan shellWaitResult, runtimeTimedOut *atomic.Bool) {
	wait := <-waitCh
	status, reason, exitCode := shellTerminal(wait.exitCode, runtimeTimedOut.Load())
	_ = jm.finalize(jobID, status, reason, exitCode)
}

func shellTerminal(exitCode int, timedOut bool) (jobstore.Status, string, *int) {
	code := exitCode
	if timedOut {
		return jobstore.StatusStopped, "run_timeout", &code
	}
	if exitCode == 0 {
		return jobstore.StatusCompleted, "exit_zero", &code
	}
	return jobstore.StatusFailed, "exit_nonzero", &code
}
