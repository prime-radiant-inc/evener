package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

const delegateFinalizeWaitTimeout = 5 * time.Second

type delegateArgs struct {
	Task            string
	AgentType       string
	Model           string
	ReasoningEffort string
	Background      bool
	BlockTimeoutMS  int
	ResultSchema    map[string]any
}

type delegateResult struct {
	JobID                 string
	Type                  string
	Status                jobstore.Status
	Reason                string
	RunningInBackground   bool
	TimedOut              bool
	TranscriptRef         string
	Output                string
	Truncated             bool
	StructuredResult      any
	StructuredResultValid bool
	Err                   error
}

func (s *Session) createDelegate(ctx context.Context, args delegateArgs) delegateResult {
	if ctx == nil {
		ctx = context.Background()
	}
	task := strings.TrimSpace(args.Task)
	if task == "" {
		return delegateStartFailed(errors.New("task is required"))
	}
	jm, err := sessionJobManager(s)
	if err != nil {
		return delegateStartFailed(err)
	}

	blockTimeout := time.Duration(clampShellBlockTimeoutMS(args.BlockTimeoutMS)) * time.Millisecond
	if len(args.ResultSchema) > 0 {
		ctx = context.WithValue(ctx, ctxCommunicateOutputSchema, args.ResultSchema)
	}

	spawned, err := s.spawnAgent(ctx, task, args.Model, "", 0, args.AgentType, args.ReasoningEffort, nil, nil)
	if err != nil {
		return delegateStartFailed(err)
	}
	childID, err := parseSpawnedAgentID(spawned)
	if err != nil {
		return delegateStartFailed(err)
	}

	sub := s.subagents.get(childID)
	if sub == nil {
		return delegateStartFailed(fmt.Errorf("spawned agent %q is not tracked", childID))
	}
	done := sub.done
	if done == nil {
		return delegateStartFailed(fmt.Errorf("spawned agent %q has no active run", childID))
	}

	run, err := s.attachDelegateJob(jm, childID, task)
	if err != nil {
		cancelDelegateChild(s, childID)
		return delegateStartFailed(err)
	}
	finalizeErr := make(chan error, 1)
	go func() {
		<-done
		finalizeErr <- s.finalizeDelegate(run.rec.JobID, childID)
	}()

	if args.Background {
		return delegateResult{
			JobID:               run.rec.JobID,
			Type:                string(jobstore.JobDelegate),
			Status:              jobstore.StatusRunning,
			RunningInBackground: true,
			TranscriptRef:       run.rec.TranscriptRef,
		}
	}

	timer := time.NewTimer(blockTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return waitForDelegateFinalization(ctx, jm, run, finalizeErr)
	case <-timer.C:
		output, _, truncated, readErr := tailOutput(run.output, shellInlineOutputBytes)
		res := delegateResult{
			JobID:               run.rec.JobID,
			Type:                string(jobstore.JobDelegate),
			Status:              jobstore.StatusRunning,
			Reason:              "foreground_timeout",
			RunningInBackground: true,
			TimedOut:            true,
			TranscriptRef:       run.rec.TranscriptRef,
			Output:              output,
			Truncated:           truncated,
			Err:                 readErr,
		}
		return res
	}
}

func waitForDelegateFinalization(ctx context.Context, jm *jobManager, run *runningJob, finalizeErr <-chan error) delegateResult {
	timer := time.NewTimer(delegateFinalizeWaitTimeout)
	defer timer.Stop()

	select {
	case <-run.done:
		return delegateTerminalResult(jm, run)
	case err := <-finalizeErr:
		if err != nil {
			return delegateFinalizeFailedResult(run, "finalize_failed", err)
		}
		return delegateTerminalResult(jm, run)
	case <-ctx.Done():
		return delegateFinalizeFailedResult(run, "cancelled", ctx.Err())
	case <-timer.C:
		return delegateFinalizeFailedResult(run, "finalize_timeout", errors.New("delegate finalization timed out"))
	}
}

func delegateFinalizeFailedResult(run *runningJob, reason string, err error) delegateResult {
	return delegateResult{
		JobID:               run.rec.JobID,
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusFailed,
		Reason:              reason,
		RunningInBackground: true,
		TranscriptRef:       run.rec.TranscriptRef,
		Err:                 err,
	}
}

func delegateStartFailed(err error) delegateResult {
	return delegateResult{
		Type:   string(jobstore.JobDelegate),
		Status: jobstore.StatusFailed,
		Reason: "start_failed",
		Err:    err,
	}
}

func parseSpawnedAgentID(spawned any) (string, error) {
	raw, ok := spawned.(string)
	if !ok {
		return "", fmt.Errorf("spawn_agent returned %T, want JSON string", spawned)
	}
	var out struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", fmt.Errorf("parse spawn_agent result: %w", err)
	}
	if strings.TrimSpace(out.AgentID) == "" {
		return "", errors.New("spawn_agent result missing agent_id")
	}
	return out.AgentID, nil
}

func (s *Session) attachDelegateJob(jm *jobManager, childID, task string) (*runningJob, error) {
	startedAt := jm.now()
	jobID := jobstore.NewJobID()
	transcriptRef := encodeRef("", childID)
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	output, err := jobstore.OpenOutput(outputPath, 0)
	if err != nil {
		return nil, err
	}
	run := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			Status:           jobstore.StatusRunning,
			Task:             task,
			OwnerSessionID:   s.id,
			VisibleToSession: s.id,
			TranscriptRef:    transcriptRef,
			StartedAt:        startedAt,
			OutputPath:       outputPath,
		},
		output:         output,
		signal:         func() { cancelDelegateChild(s, childID) },
		done:           make(chan struct{}),
		durableStarted: true,
	}

	jm.mu.Lock()
	if jm.closing {
		jm.mu.Unlock()
		_ = output.Close()
		_ = os.Remove(outputPath)
		return nil, errJobManagerClosing
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            run.rec.JobID,
		Type:             run.rec.Type,
		Task:             run.rec.Task,
		OwnerSessionID:   run.rec.OwnerSessionID,
		VisibleToSession: run.rec.VisibleToSession,
		StartedAt:        &startedAt,
		OutputPath:       run.rec.OutputPath,
	}); err != nil {
		jm.mu.Unlock()
		_ = output.Close()
		_ = os.Remove(outputPath)
		return nil, err
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:          jobstore.EventJobSessionAssigned,
		TS:            startedAt,
		JobID:         run.rec.JobID,
		TranscriptRef: run.rec.TranscriptRef,
	}); err != nil {
		jm.mu.Unlock()
		_ = output.Close()
		_ = os.Remove(outputPath)
		return nil, err
	}
	jm.running[run.rec.JobID] = run
	jm.mu.Unlock()
	return run, nil
}

func cancelDelegateChild(s *Session, childID string) {
	if s == nil {
		return
	}
	sub := s.subagents.get(childID)
	if sub == nil {
		return
	}
	sub.mu.Lock()
	sub.cancelRequested = true
	cancel := sub.cancel
	sub.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Session) finalizeDelegate(jobID, childID string) error {
	jm, err := sessionJobManager(s)
	if err != nil {
		return err
	}
	sub := s.subagents.get(childID)
	if sub == nil {
		return jm.finalize(jobID, jobstore.StatusFailed, "child_missing", nil)
	}

	sub.mu.Lock()
	status := sub.status
	prose := sub.result
	if strings.TrimSpace(prose) == "" && sub.err != nil {
		prose = sub.err.Error()
	}
	childSess := sub.sess
	sub.mu.Unlock()

	var structured any
	if childSess != nil {
		structured = childSess.CommunicateStructured()
	}

	jm.mu.Lock()
	run := jm.running[jobID]
	if run != nil {
		run.structured = structured
	}
	jm.mu.Unlock()
	appendDelegateOutput(run, prose)

	jobStatus, reason := delegateTerminalStatus(jm, run, status)
	return jm.finalize(jobID, jobStatus, reason, nil)
}

func appendDelegateOutput(run *runningJob, prose string) {
	if run == nil || run.output == nil {
		return
	}
	if strings.TrimSpace(prose) == "" {
		return
	}
	if !strings.HasSuffix(prose, "\n") {
		prose += "\n"
	}
	_, _ = run.output.Append([]byte(prose))
}

func delegateTerminalStatus(jm *jobManager, run *runningJob, status SubagentStatus) (jobstore.Status, string) {
	if jm != nil && run != nil {
		jm.mu.Lock()
		stopStatus, stopReason := run.stopStatus, run.stopReason
		jm.mu.Unlock()
		if stopStatus != "" {
			return stopStatus, stopReason
		}
	}

	switch status {
	case SubagentCompleted:
		return jobstore.StatusCompleted, ""
	case SubagentFailed:
		return jobstore.StatusFailed, ""
	case SubagentCancelled:
		return jobstore.StatusCancelled, "stopped_by_parent"
	default:
		return jobstore.StatusFailed, "unknown_child_status"
	}
}

func delegateTerminalResult(jm *jobManager, run *runningJob) delegateResult {
	rec, err := findJobRecord(jm, run.rec.JobID)
	if err != nil {
		return delegateResult{
			JobID:         run.rec.JobID,
			Type:          string(jobstore.JobDelegate),
			Status:        jobstore.StatusFailed,
			Reason:        "read_failed",
			TranscriptRef: run.rec.TranscriptRef,
			Err:           err,
		}
	}
	output, _, truncated, err := jm.readOutput(rec.JobID, shellInlineOutputBytes)
	jm.mu.Lock()
	structured := run.structured
	jm.mu.Unlock()
	return delegateResult{
		JobID:                 rec.JobID,
		Type:                  string(rec.Type),
		Status:                rec.Status,
		Reason:                rec.Reason,
		RunningInBackground:   false,
		TranscriptRef:         rec.TranscriptRef,
		Output:                output,
		Truncated:             truncated,
		StructuredResult:      structured,
		StructuredResultValid: structured != nil,
		Err:                   err,
	}
}
