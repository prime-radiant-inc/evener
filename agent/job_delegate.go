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
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
)

const (
	delegateFinalizeWaitTimeout = 5 * time.Second
	delegateFinalizeRetryDelay  = 20 * time.Millisecond

	runtimeMessageAliasCaller  = "caller"
	runtimeMessageAliasWatched = "watched"

	notResumableMissingDelegateResumeMetadata = "missing_delegate_resume_metadata"
	notResumableParentLinkageUnavailable      = "parent_linkage_unavailable"
	notResumableMissingChildSessionMeta       = "missing_child_session_meta"
	notResumableCorruptChildSessionMeta       = "corrupt_child_session_meta"
	notResumableMissingChildTranscript        = "missing_child_transcript"
	notResumableCorruptChildTranscript        = "corrupt_child_transcript"
	notResumableTranscriptSessionMismatch     = "transcript_session_mismatch"
	notResumableChildSessionBusy              = "child_session_busy"
	notResumableProfileUnavailable            = "profile_unavailable"
)

type DelegateResumability struct {
	Resumable bool
	Reason    string
	Preflight *delegateRestorePreflight
}

type delegateRestorePreflight struct {
	Meta       schema.SessionMeta
	Transcript transcriptData
	History    []schema.Turn
	Profile    *provider.Profile
}

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
	JobID                    string
	Type                     string
	Status                   jobstore.Status
	Reason                   string
	RunningInBackground      bool
	TimedOut                 bool
	TranscriptRef            string
	Output                   string
	Truncated                bool
	StructuredResult         any
	StructuredResultValid    bool
	StructuredResultValidSet bool
	StructuredResultReason   string
	Err                      error
}

type sendMessageArgs struct {
	Target         string
	Message        string
	OnFinished     string
	Background     bool
	BackgroundSet  bool
	BlockTimeoutMS int
	FromWatch      bool
}

type sendMessageResult struct {
	Target                    string
	JobID                     string
	Type                      string
	Status                    jobstore.Status
	Reason                    string
	RunningInBackground       bool
	TimedOut                  bool
	Action                    string
	ResumedFromJobID          string
	TranscriptRef             string
	Output                    string
	Truncated                 bool
	StructuredResult          any
	StructuredResultValid     bool
	StructuredResultValidSet  bool
	StructuredResultReason    string
	Delivered                 bool
	MessageType               string
	WatchSendDeliveryClass    watchSendDeliveryClass
	WatchSendDeliveryClassSet bool
	Err                       error
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

	jobID := jobstore.NewJobID()
	ctx = context.WithValue(ctx, ctxParentJobID, jobID)
	prepared, err := s.prepareSubagentRun(ctx, task, args.Model, "", 0, args.AgentType, args.ReasoningEffort, nil, nil)
	if err != nil {
		return delegateStartFailed(err)
	}
	childID := prepared.sub.id
	sub := prepared.sub
	run, err := s.attachDelegateJobWithPrepared(jm, childID, task, sub, jobID, args.ResultSchema, false, prepared)
	if err != nil {
		prepared.runCancel()
		sub.sess.Close()
		return delegateStartFailed(err)
	}
	if err := s.trackAndLaunchPreparedSubagent(prepared); err != nil {
		prepared.runCancel()
		sub.sess.Close()
		_ = jm.finalize(run.rec.JobID, jobstore.StatusFailed, "start_failed", nil)
		return delegateStartFailed(err)
	}
	finalizeErr, done := s.bridgeDelegateFinalization(run.rec.JobID, childID, sub)

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

func (s *Session) sendDelegateMessage(ctx context.Context, args sendMessageArgs) sendMessageResult {
	if ctx == nil {
		ctx = context.Background()
	}
	background := true
	if args.BackgroundSet {
		background = args.Background
	}
	target := strings.TrimSpace(args.Target)
	message := strings.TrimSpace(args.Message)
	if target == "" {
		return sendMessageFailed(target, errors.New("target is required"))
	}
	if message == "" {
		return sendMessageFailed(target, errors.New("message is required"))
	}
	if args.BlockTimeoutMS < 0 {
		return sendMessageFailed(target, errors.New("block_timeout_ms must be non-negative"))
	}
	if isRuntimeMessageAlias(target) {
		if steer := s.cfg.spawn.parentSteer; steer != nil {
			steer(message)
		} else {
			s.Steer(message)
		}
		return sendMessageResult{
			Target:      target,
			Action:      "sent",
			Delivered:   true,
			MessageType: "runtime",
		}
	}
	if isUnsupportedRuntimeMessageAlias(target) {
		return sendMessageFailed(target, fmt.Errorf("target_not_found: job %q not found", target))
	}

	s.mu.Lock()
	depth := s.depth
	s.mu.Unlock()
	if depth > 0 {
		return sendMessageFailed(target, errors.New("not_controllable: concrete delegate job targets are root-only"))
	}

	jm, err := sessionJobManager(s)
	if err != nil {
		return sendMessageFailed(target, err)
	}
	rec, err := findJobRecord(jm, target)
	if err != nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_found: %w", err))
	}
	if rec.Type != jobstore.JobDelegate {
		return sendMessageFailed(target, fmt.Errorf("target_not_messageable: job %q has type %q", target, rec.Type))
	}
	if rec.Status == jobstore.StatusRunning {
		_, childID, err := decodeRef(rec.TranscriptRef)
		if err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable: invalid transcript_ref for job %q: %w", target, err))
		}
		sub := s.subagents.get(childID)
		if sub == nil || sub.sess == nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate session %q is not retained", childID))
		}
		sub.mu.Lock()
		running := sub.running
		sub.mu.Unlock()
		if running {
			return s.sendRunningDelegateMessage(target, message, rec, args.FromWatch)
		}
		if strings.TrimSpace(args.OnFinished) == "fail" {
			return sendMessageFailed(target, fmt.Errorf("target_terminal: delegate job %q is %s", target, rec.Status))
		}
		if err := s.finalizeDelegate(rec.JobID, childID, sub); err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable: finalize observed-terminal delegate job %q: %w", target, err))
		}
		rec, err = findJobRecord(jm, target)
		if err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_found: %w", err))
		}
	}
	if !rec.Status.IsTerminal() {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate job %q has status %q", target, rec.Status))
	}
	if strings.TrimSpace(args.OnFinished) == "fail" {
		return sendMessageFailed(target, fmt.Errorf("target_terminal: delegate job %q is %s", target, rec.Status))
	}
	if isRuntimeLostDelegate(rec) {
		assessment := s.AssessDelegateResumability(rec)
		if !assessment.Resumable {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable:%s", assessment.Reason))
		}
	}

	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: invalid transcript_ref for job %q: %w", target, err))
	}
	sub := s.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		sub, err = s.restoreTerminalDelegateChild(rec, childID)
		if err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate session %q is not retained: %w", childID, err))
		}
	}

	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()
	if running {
		active, err := findRunningDelegateByTranscriptRef(jm, rec.TranscriptRef)
		if err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate session %q is running but active job is unknown: %w", childID, err))
		}
		return s.sendRunningDelegateMessage(target, message, active, args.FromWatch)
	}

	run, finalizeErr, active, err := s.resumeOrFindRunningDelegate(jm, childID, message, sub, rec.TranscriptRef, delegateResultSchema(rec), rec.DelegateRestore, args.FromWatch)
	if err != nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: resume delegate session %q: %w", childID, err))
	}
	if active != nil {
		return s.sendRunningDelegateMessage(target, message, active, args.FromWatch)
	}
	if !background {
		return waitForResumedDelegateResult(ctx, jm, target, rec.JobID, run, finalizeErr, args.BlockTimeoutMS)
	}
	return sendMessageResult{
		Target:              target,
		JobID:               run.rec.JobID,
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusRunning,
		RunningInBackground: true,
		Action:              "resumed",
		ResumedFromJobID:    rec.JobID,
		TranscriptRef:       run.rec.TranscriptRef,
	}
}

func isRuntimeLostDelegate(rec *jobstore.JobRecord) bool {
	return rec != nil &&
		rec.Type == jobstore.JobDelegate &&
		rec.Status == jobstore.StatusStopped &&
		rec.Reason == "runtime_lost"
}

func (s *Session) AssessDelegateResumability(rec *jobstore.JobRecord) DelegateResumability {
	if s == nil || rec == nil || rec.Type != jobstore.JobDelegate {
		return DelegateResumability{Reason: notResumableMissingDelegateResumeMetadata}
	}
	desc := rec.DelegateRestore
	if desc == nil {
		return DelegateResumability{Reason: notResumableMissingDelegateResumeMetadata}
	}
	childID := strings.TrimSpace(desc.ChildSessionID)
	if childID == "" || desc.TranscriptRef != rec.TranscriptRef ||
		desc.ParentSessionID != s.ID() ||
		desc.ParentJobID != rec.JobID ||
		desc.OwnerSessionID != rec.OwnerSessionID ||
		desc.VisibleSessionID != rec.VisibleToSession {
		return DelegateResumability{Reason: notResumableParentLinkageUnavailable}
	}
	if _, transcriptChildID, err := decodeRef(rec.TranscriptRef); err != nil || transcriptChildID != childID {
		return DelegateResumability{Reason: notResumableParentLinkageUnavailable}
	}
	if s.stateDir == "" {
		return DelegateResumability{Reason: notResumableMissingChildSessionMeta}
	}

	meta, err := schema.LoadSessionMeta(s.stateDir, childID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DelegateResumability{Reason: notResumableMissingChildSessionMeta}
		}
		return DelegateResumability{Reason: notResumableCorruptChildSessionMeta}
	}

	transcriptPath := filepath.Join(s.stateDir, sessionsSubdir, childID+".transcript.jsonl")
	if _, err := os.Stat(transcriptPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DelegateResumability{Reason: notResumableMissingChildTranscript}
		}
		return DelegateResumability{Reason: notResumableCorruptChildTranscript}
	}
	transcriptData, err := readStrictChildTranscript(transcriptPath, childID)
	if err != nil {
		if strings.Contains(err.Error(), notResumableTranscriptSessionMismatch) {
			return DelegateResumability{Reason: notResumableTranscriptSessionMismatch}
		}
		return DelegateResumability{Reason: notResumableCorruptChildTranscript}
	}

	if sub := s.subagents.get(childID); sub != nil {
		sub.mu.Lock()
		running := sub.running
		sub.mu.Unlock()
		if running {
			return DelegateResumability{Reason: notResumableChildSessionBusy}
		}
	}

	profile, err := s.resolveDelegateRestoreProfile(meta, desc)
	if err != nil {
		return DelegateResumability{Reason: notResumableProfileUnavailable}
	}
	return DelegateResumability{
		Resumable: true,
		Preflight: &delegateRestorePreflight{
			Meta:       meta,
			Transcript: transcriptData,
			History:    ResumeHistory(transcriptData.Entries),
			Profile:    profile,
		},
	}
}

func (s *Session) resolveDelegateRestoreProfile(meta schema.SessionMeta, desc *jobstore.DelegateRestoreDescriptor) (*provider.Profile, error) {
	if s == nil || s.profile == nil {
		return nil, errors.New("profile unavailable")
	}
	ref := strings.TrimSpace(meta.Model)
	if ref == "" && desc != nil {
		ref = strings.TrimSpace(desc.ResolvedModel)
	}
	if desc != nil && strings.TrimSpace(desc.ResolvedProfileID) != "" && strings.TrimSpace(desc.ResolvedModel) != "" {
		if desc.ResolvedProfileID != s.profile.ID() {
			ref = strings.TrimSpace(desc.ResolvedProfileID) + "/" + strings.TrimSpace(desc.ResolvedModel)
		}
	}
	if ref == "" {
		return s.profile, nil
	}
	resolved, crossProvider, err := s.resolveProfileForRef(s.profile, ref)
	if err != nil {
		return nil, err
	}
	if crossProvider {
		resolved = resolved.WithCommunicateOverridesFrom(s.profile)
	}
	return resolved, nil
}

func (s *Session) restoreTerminalDelegateChild(rec *jobstore.JobRecord, childID string) (*subagent, error) {
	if s == nil || s.subagents == nil || rec == nil {
		return nil, errors.New("delegate session restore unavailable")
	}
	if rec.Resumable == nil || !*rec.Resumable {
		return nil, errors.New("delegate job is not resumable")
	}
	if rec.DelegateRestore == nil {
		return nil, errors.New("missing delegate restore descriptor")
	}
	if strings.TrimSpace(rec.DelegateRestore.ChildSessionID) != childID {
		return nil, fmt.Errorf("restore descriptor child %q does not match transcript child %q", rec.DelegateRestore.ChildSessionID, childID)
	}
	if s.stateDir == "" {
		return nil, errors.New("state directory is not configured")
	}
	meta, err := schema.LoadSessionMeta(s.stateDir, childID)
	if err != nil {
		return nil, err
	}
	profile := s.profile
	if model := strings.TrimSpace(meta.Model); model != "" {
		resolved, crossProvider, err := s.resolveProfileForRef(s.profile, model)
		if err != nil {
			return nil, err
		}
		if crossProvider {
			resolved = resolved.WithCommunicateOverridesFrom(s.profile)
		}
		profile = resolved
	}
	restoreCfg := RestoreSessionConfig{
		StateDir:       s.stateDir,
		ResolveProfile: s.resolveProfile,
		ModelFallbacks: append([]string(nil), s.cfg.ModelFallbacks...),
		LLMRetryPolicy: s.cfg.LLMRetryPolicy,
		LLMSleep:       s.cfg.LLMSleep,
		spawn: spawnConfig{
			parentSessionID: s.id,
			subagentTask:    rec.DelegateRestore.Task,
			depth:           s.depth + 1,
			parentSteer:     s.Steer,
		},
	}
	child, err := RestoreSessionFromMetaWithConfig(s.client, profile, s.env, meta, restoreCfg)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	status := subagentStatusFromJobStatus(rec.Status)
	sub := &subagent{
		id:           childID,
		sess:         child,
		emit:         s.emit,
		status:       status,
		nudgeEnabled: true,
		agentType:    rec.DelegateRestore.AgentType,
		createdAt:    now,
		startedAt:    now,
		endedAt:      &now,
	}
	s.subagents.track(sub)
	return sub, nil
}

func subagentStatusFromJobStatus(status jobstore.Status) SubagentStatus {
	switch status {
	case jobstore.StatusCompleted:
		return SubagentCompleted
	case jobstore.StatusCancelled:
		return SubagentCancelled
	case jobstore.StatusFailed:
		return SubagentFailed
	default:
		return SubagentFailed
	}
}

func findRunningDelegateByTranscriptRef(jm *jobManager, transcriptRef string) (*jobstore.JobRecord, error) {
	jobs, err := jm.listWithError(listFilter{
		Type:   jobstore.JobDelegate,
		Status: jobstore.StatusRunning,
	})
	if err != nil {
		return nil, err
	}
	var found *jobstore.JobRecord
	for _, rec := range jobs {
		if rec.TranscriptRef == transcriptRef {
			if found != nil {
				return nil, fmt.Errorf("active_delegate_ambiguous: multiple running delegate jobs with transcript_ref %q", transcriptRef)
			}
			found = rec
		}
	}
	if found == nil {
		return nil, fmt.Errorf("active_delegate_not_found: no running delegate job with transcript_ref %q", transcriptRef)
	}
	return found, nil
}

func (s *Session) sendRunningDelegateMessage(target, message string, rec *jobstore.JobRecord, fromWatch bool) sendMessageResult {
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: invalid transcript_ref for job %q: %w", target, err))
	}
	sub := s.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate session %q is not retained", childID))
	}

	var run *runningJob
	if fromWatch {
		if jm, err := sessionJobManager(s); err == nil {
			jm.mu.Lock()
			run = jm.running[rec.JobID]
			jm.mu.Unlock()
		} else {
			return sendMessageFailed(target, err)
		}
	}

	sub.mu.Lock()
	running := sub.running
	if !running {
		sub.mu.Unlock()
		return sendMessageFailed(target, fmt.Errorf("not_controllable: delegate job %q is running but session %q is not live", target, childID))
	}
	if fromWatch {
		if run == nil {
			sub.mu.Unlock()
			return sendMessageFailed(target, fmt.Errorf("not_controllable: delegate job %q is running but runtime job is not live", target))
		}
		if rec.Status == jobstore.StatusRunning {
			sub.mu.Unlock()
			return sendMessageResult{
				Target:                    target,
				JobID:                     rec.JobID,
				Type:                      string(jobstore.JobDelegate),
				Status:                    jobstore.StatusRunning,
				RunningInBackground:       true,
				Action:                    "busy",
				WatchSendDeliveryClass:    watchSendBusy,
				WatchSendDeliveryClassSet: true,
			}
		}
		run.fromWatch.Store(true)
		sub.runFromWatch = true
	}

	sub.sess.Steer(message)
	sub.mu.Unlock()
	return sendMessageResult{
		Target:              target,
		JobID:               rec.JobID,
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusRunning,
		RunningInBackground: true,
		Action:              "sent",
		TranscriptRef:       rec.TranscriptRef,
	}
}

func (s *Session) resumeOrFindRunningDelegate(jm *jobManager, childID, message string, sub *subagent, transcriptRef string, resultSchema any, restore *jobstore.DelegateRestoreDescriptor, fromWatch bool) (*runningJob, <-chan error, *jobstore.JobRecord, error) {
	sub.mu.Lock()
	if sub.running {
		sub.mu.Unlock()
		active, err := findRunningDelegateByTranscriptRef(jm, transcriptRef)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("delegate session %q is running but active job is unknown: %w", childID, err)
		}
		return nil, nil, active, nil
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	resumeTime := time.Now()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		sub.mu.Unlock()
		runCancel()
		return nil, nil, nil, errors.New("session is closed")
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()

	run, err := s.attachDelegateJobFromWatch(jm, childID, message, sub, resultSchema, restore, fromWatch)
	if err != nil {
		sub.mu.Unlock()
		runCancel()
		s.sendersWG.Done()
		return nil, nil, nil, err
	}
	resetSubagentForRunLockedFromWatch(sub, runCancel, resumeTime, fromWatch)
	done := sub.done
	sub.mu.Unlock()

	finalizeErr := make(chan error, 1)
	go func() {
		<-done
		finalizeErr <- s.finalizeDelegate(run.rec.JobID, childID, sub)
	}()
	s.launchSubagentRun(runCtx, sub, runCancel, message, fromWatch)
	return run, finalizeErr, nil, nil
}

func sendMessageFailed(target string, err error) sendMessageResult {
	return sendMessageResult{
		Target:                    target,
		WatchSendDeliveryClass:    watchSendHardFailure,
		WatchSendDeliveryClassSet: true,
		Err:                       err,
	}
}

func isRuntimeMessageAlias(target string) bool {
	return target == runtimeMessageAliasCaller
}

func isUnsupportedRuntimeMessageAlias(target string) bool {
	switch target {
	case "main", runtimeMessageAliasWatched:
		return true
	default:
		return false
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

func waitForResumedDelegateResult(ctx context.Context, jm *jobManager, target, resumedFromJobID string, run *runningJob, finalizeErr <-chan error, blockTimeoutMS int) sendMessageResult {
	blockTimeout := time.Duration(clampShellBlockTimeoutMS(blockTimeoutMS)) * time.Millisecond
	timer := time.NewTimer(blockTimeout)
	defer timer.Stop()

	var res delegateResult
	select {
	case <-run.done:
		res = delegateTerminalResult(jm, run)
	case err := <-finalizeErr:
		if err != nil {
			res = delegateFinalizeFailedResult(run, "finalize_failed", err)
			break
		}
		res = delegateTerminalResult(jm, run)
	case <-timer.C:
		output, _, truncated, readErr := tailOutput(run.output, shellInlineOutputBytes)
		return sendMessageResult{
			Target:              target,
			JobID:               run.rec.JobID,
			Type:                string(jobstore.JobDelegate),
			Status:              jobstore.StatusRunning,
			Reason:              "foreground_timeout",
			RunningInBackground: true,
			TimedOut:            true,
			Action:              "resumed",
			ResumedFromJobID:    resumedFromJobID,
			TranscriptRef:       run.rec.TranscriptRef,
			Output:              output,
			Truncated:           truncated,
			Err:                 readErr,
		}
	case <-ctx.Done():
		res = delegateFinalizeFailedResult(run, "cancelled", ctx.Err())
	}
	return sendMessageResultFromDelegateResult(target, resumedFromJobID, "resumed", res)
}

func sendMessageResultFromDelegateResult(target, resumedFromJobID, action string, res delegateResult) sendMessageResult {
	return sendMessageResult{
		Target:                   target,
		JobID:                    res.JobID,
		Type:                     res.Type,
		Status:                   res.Status,
		Reason:                   res.Reason,
		RunningInBackground:      res.RunningInBackground,
		TimedOut:                 res.TimedOut,
		Action:                   action,
		ResumedFromJobID:         resumedFromJobID,
		TranscriptRef:            res.TranscriptRef,
		Output:                   res.Output,
		Truncated:                res.Truncated,
		StructuredResult:         res.StructuredResult,
		StructuredResultValid:    res.StructuredResultValid,
		StructuredResultValidSet: res.StructuredResultValidSet,
		StructuredResultReason:   res.StructuredResultReason,
		Err:                      res.Err,
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
		return "", fmt.Errorf("delegate runtime returned %T, want JSON string", spawned)
	}
	var out struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", fmt.Errorf("parse delegate runtime result: %w", err)
	}
	if strings.TrimSpace(out.AgentID) == "" {
		return "", errors.New("delegate runtime result missing child session id")
	}
	return out.AgentID, nil
}

func (s *Session) attachAndBridgeDelegateJob(jm *jobManager, childID, task string, sub *subagent, jobID string, resultSchema any) (*runningJob, <-chan error, <-chan struct{}, error) {
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	if done == nil {
		return nil, nil, nil, fmt.Errorf("delegate session %q has no active run", childID)
	}
	run, err := s.attachDelegateJobWithID(jm, childID, task, sub, jobID, resultSchema, false)
	if err != nil {
		return nil, nil, nil, err
	}
	finalizeErr, bridgedDone := s.bridgeDelegateFinalization(run.rec.JobID, childID, sub)
	return run, finalizeErr, bridgedDone, nil
}

func (s *Session) bridgeDelegateFinalization(jobID, childID string, sub *subagent) (<-chan error, <-chan struct{}) {
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	finalizeErr := make(chan error, 1)
	go func() {
		<-done
		finalizeErr <- s.finalizeDelegate(jobID, childID, sub)
	}()
	return finalizeErr, done
}

func (s *Session) attachDelegateJob(jm *jobManager, childID, task string, sub *subagent) (*runningJob, error) {
	return s.attachDelegateJobWithID(jm, childID, task, sub, jobstore.NewJobID(), nil, false)
}

func (s *Session) attachDelegateJobFromWatch(jm *jobManager, childID, task string, sub *subagent, resultSchema any, restore *jobstore.DelegateRestoreDescriptor, fromWatch bool) (*runningJob, error) {
	return s.attachDelegateJobWithRestore(jm, childID, task, sub, jobstore.NewJobID(), resultSchema, fromWatch, nil, restore)
}

func (s *Session) attachDelegateJobWithID(jm *jobManager, childID, task string, sub *subagent, jobID string, resultSchema any, fromWatch bool) (*runningJob, error) {
	return s.attachDelegateJobWithRestore(jm, childID, task, sub, jobID, resultSchema, fromWatch, nil, nil)
}

func (s *Session) attachDelegateJobWithPrepared(jm *jobManager, childID, task string, sub *subagent, jobID string, resultSchema any, fromWatch bool, prepared *preparedSubagentRun) (*runningJob, error) {
	return s.attachDelegateJobWithRestore(jm, childID, task, sub, jobID, resultSchema, fromWatch, prepared, nil)
}

func (s *Session) attachDelegateJobWithRestore(jm *jobManager, childID, task string, sub *subagent, jobID string, resultSchema any, fromWatch bool, prepared *preparedSubagentRun, previousRestore *jobstore.DelegateRestoreDescriptor) (*runningJob, error) {
	startedAt := jm.now()
	transcriptRef := encodeRef("", childID)
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	output, err := jobstore.OpenOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		return nil, err
	}
	restore := s.delegateRestoreDescriptor(jobID, childID, task, transcriptRef, resultSchema, prepared)
	if previousRestore != nil {
		restore = s.resumedDelegateRestoreDescriptor(jobID, childID, transcriptRef, resultSchema, previousRestore)
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
			DelegateRestore:  restore,
			StartedAt:        startedAt,
			OutputPath:       outputPath,
		},
		output:         output,
		signal:         func() { cancelDelegateSub(sub) },
		done:           make(chan struct{}),
		durableStarted: true,
	}
	run.fromWatch.Store(fromWatch)

	jm.mu.Lock()
	if jm.closing {
		jm.mu.Unlock()
		_ = output.Close()
		_ = os.Remove(outputPath)
		return nil, errJobManagerClosing
	}
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            run.rec.JobID,
		Type:             run.rec.Type,
		Task:             run.rec.Task,
		OwnerSessionID:   run.rec.OwnerSessionID,
		VisibleToSession: run.rec.VisibleToSession,
		StartedAt:        &startedAt,
		OutputPath:       run.rec.OutputPath,
		TranscriptRef:    run.rec.TranscriptRef,
		DelegateRestore:  run.rec.DelegateRestore,
	}
	if err := jm.appendEvent(started); err != nil {
		jm.mu.Unlock()
		_ = output.Close()
		_ = os.Remove(outputPath)
		return nil, err
	}
	jm.running[run.rec.JobID] = run
	jm.mu.Unlock()
	jm.emitJobStarted(started, run)
	return run, nil
}

func (s *Session) delegateRestoreDescriptor(jobID, childID, task, transcriptRef string, resultSchema any, prepared *preparedSubagentRun) *jobstore.DelegateRestoreDescriptor {
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:          1,
		ChildSessionID:   childID,
		TranscriptRef:    transcriptRef,
		ParentSessionID:  s.id,
		ParentJobID:      jobID,
		OwnerSessionID:   s.id,
		VisibleSessionID: s.id,
		Task:             task,
		ResultSchema:     cloneDelegateResultSchema(resultSchema),
	}
	if prepared == nil {
		if sub := s.subagents.get(childID); sub != nil && sub.sess != nil {
			profile := sub.sess.currentProfile()
			desc.ResolvedProfileID = profile.ID()
			desc.ResolvedModel = profile.Model()
		}
		return desc
	}
	desc.ParentSessionID = prepared.parentSessionID
	desc.ParentJobID = prepared.parentJobID
	desc.OriginToolCallID = prepared.originToolCallID
	desc.Task = prepared.task
	desc.AgentType = prepared.agentType
	desc.RequestedModel = prepared.requestedModel
	desc.ReasoningEffort = prepared.reasoningEffort
	desc.AgentName = prepared.resolvedAgentName
	desc.FrozenRolePrompt = prepared.frozenRolePrompt
	desc.FrozenTaskPrompt = prepared.frozenTaskPrompt
	desc.FrozenToolNames = append([]string(nil), prepared.frozenToolNames...)
	desc.FrozenSkillNames = append([]string(nil), prepared.frozenSkillNames...)
	desc.WorkingDir = prepared.workingDir
	desc.LocalEnvPolicy = prepared.localEnvPolicy
	desc.ExplicitToolGrants = append([]string(nil), prepared.explicitToolGrants...)
	if prepared.resultSchema != nil {
		desc.ResultSchema = cloneDelegateResultSchema(prepared.resultSchema)
	}
	if prepared.sub != nil && prepared.sub.sess != nil {
		profile := prepared.sub.sess.currentProfile()
		desc.ResolvedProfileID = profile.ID()
		desc.ResolvedModel = profile.Model()
	}
	return desc
}

func (s *Session) resumedDelegateRestoreDescriptor(jobID, childID, transcriptRef string, resultSchema any, previous *jobstore.DelegateRestoreDescriptor) *jobstore.DelegateRestoreDescriptor {
	version := previous.Version
	if version == 0 {
		version = 1
	}
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:            version,
		ChildSessionID:     childID,
		TranscriptRef:      transcriptRef,
		ParentSessionID:    s.id,
		ParentJobID:        jobID,
		OwnerSessionID:     s.id,
		VisibleSessionID:   s.id,
		OriginTurnID:       previous.OriginTurnID,
		OriginToolCallID:   previous.OriginToolCallID,
		Task:               previous.Task,
		AgentType:          previous.AgentType,
		RequestedModel:     previous.RequestedModel,
		ResolvedProfileID:  previous.ResolvedProfileID,
		ResolvedModel:      previous.ResolvedModel,
		ReasoningEffort:    previous.ReasoningEffort,
		AgentName:          previous.AgentName,
		FrozenRolePrompt:   previous.FrozenRolePrompt,
		FrozenTaskPrompt:   previous.FrozenTaskPrompt,
		FrozenToolNames:    append([]string(nil), previous.FrozenToolNames...),
		FrozenSkillNames:   append([]string(nil), previous.FrozenSkillNames...),
		WorkingDir:         previous.WorkingDir,
		LocalEnvPolicy:     previous.LocalEnvPolicy,
		ResultSchema:       cloneDelegateResultSchema(previous.ResultSchema),
		ExplicitToolGrants: append([]string(nil), previous.ExplicitToolGrants...),
	}
	if resultSchema != nil {
		desc.ResultSchema = cloneDelegateResultSchema(resultSchema)
	}
	return desc
}

func cancelDelegateChild(s *Session, childID string) {
	if s == nil {
		return
	}
	sub := s.subagents.get(childID)
	if sub == nil {
		return
	}
	cancelDelegateSub(sub)
}

func cancelDelegateSub(sub *subagent) {
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

func (s *Session) finalizeDelegate(jobID, childID string, sub *subagent) error {
	jm, err := sessionJobManager(s)
	if err != nil {
		return err
	}
	if sub == nil {
		sub = s.subagents.get(childID)
	}

	for {
		err := s.finalizeDelegateOnce(jm, jobID, sub)
		if err == nil {
			return nil
		}
		if delegateFinalizeStopsRetry(jm, err) {
			jm.abandonRunningJob(jobID)
			return err
		}
		time.Sleep(delegateFinalizeRetryDelay)
	}
}

func (s *Session) finalizeDelegateOnce(jm *jobManager, jobID string, sub *subagent) error {
	return jm.finalizeWithRun(jobID, func(run *runningJob) (jobstore.Status, string, *int, error) {
		if sub == nil {
			return jobstore.StatusFailed, "child_missing", nil, nil
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
		structuredCaptureFailed := false
		if childSess != nil {
			structured = childSess.CommunicateStructured()
		} else if delegateResultSchema(run.rec) != nil {
			structuredCaptureFailed = true
		}

		output := delegateOutputBytes(prose)
		for {
			jm.mu.Lock()
			run.structured = structured
			run.structuredCaptureFailed = structuredCaptureFailed
			run.afterDurableFinish = func() {
				sub.mu.Lock()
				sub.resultConsumed = true
				sub.mu.Unlock()
			}
			outputAppended := run.delegateOutputAppended
			outputWritten := run.delegateOutputWritten
			jm.mu.Unlock()
			if outputAppended {
				break
			}
			if outputWritten >= len(output) {
				if len(output) > 0 {
					if _, err := appendDelegateOutput(jm, run, nil); err != nil {
						return "", "", nil, err
					}
				}
				jm.mu.Lock()
				run.delegateOutputAppended = true
				jm.mu.Unlock()
				break
			}
			n, err := appendDelegateOutput(jm, run, output[outputWritten:])
			if n > 0 {
				jm.mu.Lock()
				run.delegateOutputWritten += n
				outputWritten = run.delegateOutputWritten
				jm.mu.Unlock()
			}
			if err != nil {
				return "", "", nil, err
			}
			if outputWritten >= len(output) {
				jm.mu.Lock()
				run.delegateOutputAppended = true
				jm.mu.Unlock()
				break
			}
		}

		jobStatus, reason := delegateTerminalStatus(jm, run, status)
		return jobStatus, reason, nil, nil
	})
}

func delegateFinalizeStopsRetry(jm *jobManager, err error) bool {
	return errors.Is(err, errJobManagerClosing) ||
		errors.Is(err, jobstore.ErrStoreClosed) ||
		delegateJobManagerClosing(jm)
}

func delegateJobManagerClosing(jm *jobManager) bool {
	if jm == nil {
		return true
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.closing
}

func delegateOutputBytes(prose string) []byte {
	if strings.TrimSpace(prose) == "" {
		return nil
	}
	if !strings.HasSuffix(prose, "\n") {
		prose += "\n"
	}
	return []byte(prose)
}

func appendDelegateOutput(jm *jobManager, run *runningJob, b []byte) (int, error) {
	if run == nil || run.output == nil {
		return 0, nil
	}
	return jm.appendJobOutput(run.rec.JobID, run.output, b)
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
	structured := rec.StructuredResult
	structuredValid := rec.StructuredResultValid
	if structured == nil && structuredValid == nil {
		structured = run.structured
		if structured != nil && structuredValid == nil {
			valid := true
			structuredValid = &valid
		}
	}
	jm.mu.Unlock()
	valid := structuredValid != nil && *structuredValid
	return delegateResult{
		JobID:                    rec.JobID,
		Type:                     string(rec.Type),
		Status:                   rec.Status,
		Reason:                   rec.Reason,
		RunningInBackground:      false,
		TranscriptRef:            rec.TranscriptRef,
		Output:                   output,
		Truncated:                truncated,
		StructuredResult:         structured,
		StructuredResultValid:    valid,
		StructuredResultValidSet: structuredValid != nil,
		StructuredResultReason:   rec.StructuredResultReason,
		Err:                      err,
	}
}

func delegateResultSchema(rec *jobstore.JobRecord) any {
	if rec == nil || rec.DelegateRestore == nil {
		return nil
	}
	return rec.DelegateRestore.ResultSchema
}

func cloneDelegateResultSchema(schema any) any {
	if schema == nil {
		return nil
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return schema
	}
	var cloned any
	if err := json.Unmarshal(b, &cloned); err != nil {
		return schema
	}
	return cloned
}
