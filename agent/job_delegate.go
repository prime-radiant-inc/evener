package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
)

var errDelegateStartForwardFailed = errors.New("delegate start forward failed")
var errDelegateStartForwardTerminalFailed = errors.New("delegate start forward terminal append failed")

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
	notResumableWorktreeDisposed              = "isolation_worktree_disposed"
	notResumableWorkingDirMissing             = "working_dir_missing"
)

type delegateResumability struct {
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

type delegateResumabilityMode int

const (
	delegateResumabilityPreflight delegateResumabilityMode = iota
	delegateResumabilityProjection
)

// resumeHistoryForRestore builds the resume history for a delegate restore,
// using the session's override when set (tests) and the production ResumeHistory
// otherwise.
func (s *Session) resumeHistoryForRestore(entries []transcript.Entry) []schema.Turn {
	if s != nil && s.delegateRestoreResumeHistory != nil {
		return s.delegateRestoreResumeHistory(entries)
	}
	return ResumeHistory(entries)
}

type delegateArgs struct {
	Task                string
	AgentType           string
	Model               string
	ReasoningEffort     string
	Background          bool
	BlockTimeoutMS      int
	DelegationAllowance int
	WatchParent         bool
	Isolation           string
	ResultSchema        map[string]any
}

// delegateWorktreeReport is the native worktree tools spec §9 lifecycle step 3
// per-job report for an isolated delegate: the lane's path, branch, commits
// ahead of its base, and dirty state, so the parent can merge a lane's
// commits from the main root between jobs without guessing where it lives.
// nil on a non-isolated delegate or when the lane cannot be inspected.
type delegateWorktreeReport struct {
	Path   string
	Branch string
	Ahead  int
	Dirty  bool
}

type delegateResult struct {
	DelegateID               string
	StartedJobID             string
	JobID                    string
	LatestJobID              string
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
	Watching                 bool
	Watches                  []watchListEntry
	Worktree                 *delegateWorktreeReport
	Err                      error
}

type sendMessageArgs struct {
	Target         string
	Message        string
	OnIdle         string
	Background     bool
	BackgroundSet  bool
	BlockTimeoutMS int
	FromWatch      bool
	Provenance     *provenance.Causal
}

type sendMessageResult struct {
	Target                    string
	DelegateID                string
	StartedJobID              string
	JobID                     string
	LatestJobID               string
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
	Watching                  bool
	Watches                   []watchListEntry
	Worktree                  *delegateWorktreeReport
	Delivered                 bool
	MessageType               string
	WatchSendDeliveryClass    watchSendDeliveryClass
	WatchSendDeliveryClassSet bool
	WaitIgnoredReason         string
	Err                       error
}

func (s *Session) createDelegate(ctx context.Context, args delegateArgs) delegateResult {
	if ctx == nil {
		ctx = context.Background()
	}
	task := strings.TrimSpace(args.Task)
	if task == "" {
		return delegateStartFailed(errors.New("invalid_request: task is required"))
	}
	// isolation (spec §9 "Tool surface"): absent -> today's behavior; only
	// "worktree" is defined. Validated before any durable work so an
	// unsupported value never mints IDs or touches git.
	isolation := strings.TrimSpace(args.Isolation)
	if isolation != "" && isolation != "worktree" {
		return delegateStartFailed(fmt.Errorf("invalid_request: isolation %q is not supported (expected \"worktree\")", isolation))
	}
	jm, err := sessionJobManager(s)
	if err != nil {
		return delegateStartFailed(err)
	}

	// Grant rule (spec §1): a session may grant a child a delegation_allowance
	// strictly less than its own; allowance 0 = a leaf delegate. Under defaults
	// (MaxSubagentDepth=2) the root's allowance is 2, so it may grant 1 (a delegate
	// that can itself spawn leaves); deeper trees require raising the config.
	s.mu.Lock()
	ownAllowance := s.delegationAllowance
	s.mu.Unlock()
	if ok, validRange := validateDelegateGrant(args.DelegationAllowance, ownAllowance); !ok {
		return delegateStartFailed(fmt.Errorf("invalid_request: delegation_allowance must be less than your own allowance (%d); valid grants: %s", ownAllowance, validRange))
	}

	blockTimeout := time.Duration(clampShellBlockTimeoutMS(args.BlockTimeoutMS)) * time.Millisecond
	if len(args.ResultSchema) > 0 {
		ctx = context.WithValue(ctx, ctxCommunicateOutputSchema, args.ResultSchema)
	}
	ctx = context.WithValue(ctx, ctxDelegationAllowance, args.DelegationAllowance)
	if args.WatchParent {
		ctx = context.WithValue(ctx, ctxWatchParent, true)
	}

	delegateID := jobstore.NewDelegateID()
	delegateGeneration := jobstore.NewDelegateGeneration()
	jobID := jobstore.NewJobID()
	ctx = context.WithValue(ctx, ctxParentDelegateID, delegateID)
	ctx = context.WithValue(ctx, ctxParentJobID, jobID)

	// spec §9 lifecycle step 1: create the isolation lane on the PARENT side,
	// named for the delegate id, before the child spawns — its child restore
	// descriptor's WorkingDir then simply IS the lane (prepareSubagentRun's
	// workingDir override roots the child env there via WithWorkingDirectory).
	workingDir := ""
	if isolation == "worktree" {
		lanePath, _, _, _, wtErr := s.createDelegateWorktree(ctx, delegateID)
		if wtErr != nil {
			return delegateStartFailedWithIDs(delegateID, jobID, wtErr)
		}
		workingDir = lanePath
		ctx = context.WithValue(ctx, ctxIsolation, isolation)
	}
	prepared, err := s.prepareSubagentRun(ctx, task, args.Model, workingDir, 0, args.AgentType, args.ReasoningEffort, nil, nil)
	if err != nil {
		if workingDir != "" {
			s.rollbackFreshDelegateWorktree(delegateID, workingDir)
		}
		return delegateStartFailedWithIDs(delegateID, jobID, err)
	}
	childID := prepared.sub.id
	sub := prepared.sub
	run, err := s.attachDelegateJobWithPreparedAndDelegate(jm, childID, task, sub, jobID, delegateID, delegateGeneration, args.AgentType, args.ResultSchema, false, prepared)
	if err != nil {
		prepared.runCancel()
		sub.sess.Close()
		if workingDir != "" {
			s.rollbackFreshDelegateWorktree(delegateID, workingDir)
		}
		return delegateStartFailedWithIDs(delegateID, jobID, err)
	}
	if err := s.trackAndLaunchPreparedSubagent(prepared); err != nil {
		prepared.runCancel()
		sub.sess.Close()
		_ = jm.finalize(run.rec.JobID, jobstore.StatusFailed, "start_failed", nil)
		if workingDir != "" {
			s.rollbackFreshDelegateWorktree(delegateID, workingDir)
		}
		return delegateStartFailedWithIDs(delegateID, run.rec.JobID, err)
	}
	if args.Background {
		s.bridgeDelegateFinalization(run.rec.JobID, childID, sub, true)
		return delegateResult{
			DelegateID:          delegateID,
			StartedJobID:        run.rec.JobID,
			JobID:               run.rec.JobID,
			LatestJobID:         run.rec.JobID,
			Type:                string(jobstore.JobDelegate),
			Status:              jobstore.StatusRunning,
			RunningInBackground: true,
			TranscriptRef:       run.rec.TranscriptRef,
		}
	}

	done := delegateDone(sub)
	timer := s.sclock().NewTimer(blockTimeout)
	defer timer.Stop()
	select {
	case <-done:
		finalizeErr := s.bridgeDelegateFinalizationWithDone(run.rec.JobID, childID, sub, done, false)
		return waitForDelegateFinalization(ctx, s, jm, run, finalizeErr)
	case <-timer.C():
		s.bridgeDelegateFinalizationWithDone(run.rec.JobID, childID, sub, done, true)
		output, _, truncated, readErr := tailOutput(run.output, shellInlineOutputBytes)
		res := delegateResult{
			DelegateID:          delegateID,
			StartedJobID:        run.rec.JobID,
			JobID:               run.rec.JobID,
			LatestJobID:         run.rec.JobID,
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

// delegateTargetKind names how a delegate_send target should be dispatched, as
// decided by classifyDelegateSendTarget.
type delegateTargetKind int

const (
	// delegateTargetRejected: the request fails pre-dispatch validation.
	delegateTargetRejected delegateTargetKind = iota
	// delegateTargetCallerAlias: steer the message back to the caller route.
	delegateTargetCallerAlias
	// delegateTargetJobHandle: a job_-prefixed handle (not a send target).
	delegateTargetJobHandle
	// delegateTargetDelegateID: a delegate-id path (dlg_ prefix, or a bare
	// target the delegate-id handling then rejects as target_not_found).
	delegateTargetDelegateID
)

// classifyDelegateSendTarget is the pure pre-dispatch decision lifted out of
// sendDelegateMessage. Given the already-trimmed target/message, the resolved
// on_idle, the requested block timeout, and the caller's routing context, it
// decides how the target is dispatched (or why it is rejected) without touching
// the job manager or store. The wrapper performs the matching effect per kind
// and preserves the original ordering: rejections and the caller-alias steer
// happen before sessionJobManager; the job_/delegate-id handling happens after.
// A rejected reason is fully rendered (including the target where the original
// embedded it) so the wrapper can wrap it verbatim.
func classifyDelegateSendTarget(target, message, onIdle string, blockTimeoutMS int, fromWatch, hasCallerRoute bool) (kind delegateTargetKind, reason string) {
	if target == "" {
		return delegateTargetRejected, "invalid_request: target is required"
	}
	if message == "" {
		return delegateTargetRejected, "invalid_request: message is required"
	}
	if blockTimeoutMS < 0 {
		return delegateTargetRejected, "invalid_request: max_wait_ms must be non-negative"
	}
	if onIdle != "fail" && onIdle != "start" {
		return delegateTargetRejected, "invalid_request: on_idle must be start or fail"
	}
	if target == "main" || target == runtimeMessageAliasWatched {
		return delegateTargetRejected, fmt.Sprintf("invalid_request: %s is not a delegate_send target", target)
	}
	if strings.HasPrefix(target, "local:") || strings.HasPrefix(target, "proj:") {
		return delegateTargetRejected, "invalid_request: transcript_ref is an archival read handle, not a control target"
	}
	if isRuntimeMessageAlias(target) {
		if !hasCallerRoute {
			return delegateTargetRejected, "invalid_request: caller is only available from a delegate/watch-delivered context"
		}
		// Watch sends to the caller route through the notification rail
		// (drainPendingWatchSends enqueues a wake token), never here. A FromWatch
		// caller send reaching sendDelegateMessage is an internal routing bug.
		if fromWatch {
			return delegateTargetRejected, "internal: watch sends to caller route via the notification rail"
		}
		return delegateTargetCallerAlias, ""
	}
	if strings.HasPrefix(target, "job_") {
		return delegateTargetJobHandle, ""
	}
	return delegateTargetDelegateID, ""
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
	onIdle := strings.TrimSpace(args.OnIdle)
	if onIdle == "" {
		onIdle = "fail"
	}
	kind, reason := classifyDelegateSendTarget(target, message, onIdle, args.BlockTimeoutMS, args.FromWatch, s.hasCallerRoute())
	switch kind {
	case delegateTargetRejected:
		return sendMessageFailed(target, errors.New(reason))
	case delegateTargetCallerAlias:
		callerProvenance := provenance.Clone(args.Provenance)
		if callerProvenance == nil {
			callerProvenance = s.activeCausalProvenance()
		}
		delivered := true
		if steer := s.cfg.spawn.parentSteerDelivered; steer != nil {
			delivered = steer(message, callerProvenance)
		} else if steer := s.cfg.spawn.parentSteer; steer != nil {
			steer(message, callerProvenance)
		} else {
			delivered = s.trySteerWithProvenance(message, callerProvenance)
		}
		if !delivered {
			return sendMessageResult{
				Target:      target,
				Action:      "delivered",
				Delivered:   false,
				MessageType: "runtime",
				Err:         errors.New("caller unavailable"),
			}
		}
		if mark := s.cfg.spawn.parentMarkCallerCallbackDelivered; mark != nil {
			mark(s.cfg.spawn.parentJobID)
		}
		if s.currentEntryKind() == EntryWatchDelivery {
			s.markWatchCallbackDeliveredForCurrentTurn()
		}
		return sendMessageResult{
			Target:      target,
			Action:      "delivered",
			Delivered:   true,
			MessageType: "runtime",
		}
	}

	jm, err := sessionJobManager(s)
	if err != nil {
		return sendMessageFailed(target, err)
	}
	if kind == delegateTargetJobHandle {
		if rec, ok := delegateJobRecordForJobID(jm, target); ok {
			if !delegateControlOwnedBySession(rec.OwnerSessionID, s.id) {
				return sendMessageFailed(target, fmt.Errorf("not_controllable: delegate job %q is owned by descendant session %q; you may only message your own direct delegates", target, rec.OwnerSessionID))
			}
			return sendMessageFailed(target, fmt.Errorf("invalid_request: job_id is a job/turn handle; send messages to delegate_id %s", rec.DelegateID))
		}
		return sendMessageFailed(target, errors.New("invalid_request: job_id is a job/turn handle; send messages to delegate_id"))
	}
	if !strings.HasPrefix(target, "dlg_") {
		return sendMessageFailed(target, fmt.Errorf("target_not_found: delegate %q not found", target))
	}
	delegates, err := jm.store.LoadDelegates()
	if err != nil {
		return sendMessageFailed(target, err)
	}
	delegateRec := delegates[target]
	if delegateRec == nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_found: delegate %q not found", target))
	}
	delegateID := delegateRec.DelegateID
	recTarget := delegateRec.CurrentJobID
	if recTarget == "" {
		recTarget = delegateRec.LatestJobID
	}
	if recTarget == "" {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate %q has no job history", target))
	}
	rec, err := findJobRecord(jm, recTarget)
	if err != nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_found: %w", err))
	}
	// Own direct delegates at every level (spec §3): a coordinator at any depth
	// may message its own direct worker delegate by delegate_id, but the scope is the
	// session's own delegates — a forwarded copy of a deeper descendant's delegate
	// (owned by another session) is not directly controllable.
	if !delegateControlOwnedBySession(rec.OwnerSessionID, s.id) {
		return sendMessageFailed(target, fmt.Errorf("not_controllable: delegate job %q is owned by descendant session %q; you may only message your own direct delegates", target, rec.OwnerSessionID))
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
		running := sub.running || sub.driving
		sub.mu.Unlock()
		if running {
			// A driving child is in flight: steer into the drive turn (spec §3, A7).
			// sendRunningDelegateMessage takes the StatusRunning rec directly (no
			// findRunningDelegate lookup), so the drive turn's missing job record
			// does not strand the steer.
			return s.sendRunningDelegateMessage(target, message, rec, args.FromWatch, args.Provenance)
		}
		if err := s.finalizeDelegate(rec.JobID, childID, sub); err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable: finalize observed-terminal delegate job %q: %w", target, err))
		}
		rec, err = findJobRecord(jm, rec.JobID)
		if err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_found: %w", err))
		}
	}
	if !rec.Status.IsTerminal() {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate job %q has status %q", target, rec.Status))
	}
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: invalid transcript_ref for job %q: %w", target, err))
	}
	sub := s.subagents.get(childID)
	if sub != nil && sub.sess != nil {
		sub.mu.Lock()
		running := sub.running
		driving := sub.driving
		sub.mu.Unlock()
		if driving && !running {
			return s.sendRunningDelegateMessage(target, message, rec, args.FromWatch, args.Provenance)
		}
		if running {
			active, err := findRunningDelegateByTranscriptRef(jm, rec.TranscriptRef)
			if err != nil {
				if onIdle == "fail" {
					return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate session %q is running but active job is unknown: %w", childID, err))
				}
			} else {
				return s.sendRunningDelegateMessage(target, message, active, args.FromWatch, args.Provenance)
			}
		}
	}
	if onIdle == "fail" {
		return sendMessageFailed(target, fmt.Errorf("target_idle: delegate %q is idle; pass on_idle=\"start\" to start the next job", target))
	}
	var restorePreflight *delegateRestorePreflight
	if isRuntimeLostDelegate(rec) {
		assessment := s.assessDelegateResumability(rec, delegateResumabilityPreflight)
		if !assessment.Resumable {
			return sendMessageFailed(target, notResumableSendError(assessment.Reason))
		}
		restorePreflight = assessment.Preflight
	}
	if sub == nil || sub.sess == nil {
		if restorePreflight == nil {
			assessment := s.assessDelegateResumability(rec, delegateResumabilityPreflight)
			if !assessment.Resumable {
				return sendMessageFailed(target, notResumableSendError(assessment.Reason))
			}
			restorePreflight = assessment.Preflight
		}
		sub, err = s.restoreTerminalDelegateChild(rec, childID, restorePreflight)
		if err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate session %q is not retained: %w", childID, err))
		}
	}

	sub.mu.Lock()
	running := sub.running
	driving := sub.driving
	sub.mu.Unlock()
	if driving && !running {
		// A drive turn is in flight (sub.driving==true) but mints no running job
		// record (the EntryNotification turn is not a watch-tracked job), so the
		// findRunningDelegateByTranscriptRef lookup below would find nothing. Steer
		// the message into the in-flight drive turn using the terminal rec we
		// already hold; sendRunningDelegateMessage's driving-aware guard delivers it
		// via trySteer (spec §3: a mid-drive send is absorbed by the single
		// in-flight turn, never a second concurrent ProcessInputKind).
		return s.sendRunningDelegateMessage(target, message, rec, args.FromWatch, args.Provenance)
	}
	if running {
		active, err := findRunningDelegateByTranscriptRef(jm, rec.TranscriptRef)
		if err != nil {
			return sendMessageFailed(target, fmt.Errorf("target_not_resumable: delegate session %q is running but active job is unknown: %w", childID, err))
		}
		return s.sendRunningDelegateMessage(target, message, active, args.FromWatch, args.Provenance)
	}

	messageProvenance := provenance.Clone(args.Provenance)
	if messageProvenance == nil {
		messageProvenance = s.activeCausalProvenance()
	}
	run, finalizeErr, active, err := s.resumeOrFindRunningDelegate(jm, childID, message, sub, rec.TranscriptRef, delegateID, delegateResultSchema(rec), rec.DelegateRestore, args.FromWatch, messageProvenance)
	if err != nil {
		return sendMessageFailed(target, fmt.Errorf("target_not_resumable: resume delegate session %q: %w", childID, err))
	}
	if active != nil {
		return s.sendRunningDelegateMessage(target, message, active, args.FromWatch, args.Provenance)
	}
	if !background {
		return waitForResumedDelegateResult(ctx, s, jm, target, rec.JobID, run, finalizeErr, args.BlockTimeoutMS)
	}
	return sendMessageResult{
		Target:              target,
		DelegateID:          delegateID,
		StartedJobID:        run.rec.JobID,
		JobID:               run.rec.JobID,
		LatestJobID:         run.rec.JobID,
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusRunning,
		RunningInBackground: true,
		Action:              "started",
		ResumedFromJobID:    rec.JobID,
		TranscriptRef:       run.rec.TranscriptRef,
	}
}

// notResumableSendError maps an assessDelegateResumability reason code to the
// error delegate_send surfaces to the model. The two native-worktree disposal
// reasons (spec §9 step 5) get a clear, actionable message; every other reason
// keeps the machine-readable "target_not_resumable:<code>" shape.
func notResumableSendError(reason string) error {
	switch reason {
	case notResumableWorktreeDisposed:
		return errors.New("target_not_resumable: this delegate's isolation worktree was disposed at session close; start a new delegate")
	case notResumableWorkingDirMissing:
		return errors.New("target_not_resumable: this delegate's working directory no longer exists; start a new delegate")
	default:
		return fmt.Errorf("target_not_resumable:%s", reason)
	}
}

func isRuntimeLostDelegate(rec *jobstore.JobRecord) bool {
	return rec != nil &&
		rec.Type == jobstore.JobDelegate &&
		rec.Status == jobstore.StatusStopped &&
		rec.Reason == "runtime_lost"
}

// validateDelegateRestoreState runs the pure record/descriptor validation gate
// that precedes any on-disk restore work: parent linkage, transcript-ref shape,
// env/working-dir policy, frozen-skill consistency, and state-dir presence. It
// returns the notResumable reason for the first failing check, or "" when every
// pure precondition holds and the caller should proceed to load the child's
// on-disk session meta and transcript. It performs no I/O, locking, or mutation:
// parentSessionID and hasStateDir are snapshots supplied by the caller.
func validateDelegateRestoreState(rec *jobstore.JobRecord, parentSessionID string, hasStateDir bool) string {
	if rec == nil || rec.Type != jobstore.JobDelegate {
		return notResumableMissingDelegateResumeMetadata
	}
	desc := rec.DelegateRestore
	if desc == nil {
		return notResumableMissingDelegateResumeMetadata
	}
	childID := strings.TrimSpace(desc.ChildSessionID)
	if childID == "" || desc.TranscriptRef != rec.TranscriptRef ||
		desc.ParentSessionID != parentSessionID ||
		desc.ParentJobID != rec.JobID ||
		desc.OwnerSessionID != rec.OwnerSessionID ||
		desc.VisibleSessionID != rec.VisibleToSession {
		return notResumableParentLinkageUnavailable
	}
	if _, transcriptChildID, err := decodeRef(rec.TranscriptRef); err != nil || transcriptChildID != childID {
		return notResumableParentLinkageUnavailable
	}
	if !hasValidDelegateRestoreLocalEnvPolicy(desc) {
		return notResumableParentLinkageUnavailable
	}
	if !hasValidDelegateRestoreWorkingDir(desc) {
		return notResumableParentLinkageUnavailable
	}
	if _, err := restoreFrozenSkillBodies(desc.FrozenSkillNames, desc.FrozenSkillBodies); err != nil {
		return notResumableCorruptChildSessionMeta
	}
	if !hasStateDir {
		return notResumableMissingChildSessionMeta
	}
	return ""
}

func (s *Session) assessDelegateResumability(rec *jobstore.JobRecord, mode delegateResumabilityMode) delegateResumability {
	if s == nil {
		return delegateResumability{Reason: notResumableMissingDelegateResumeMetadata}
	}
	if reason := validateDelegateRestoreState(rec, s.ID(), s.stateDir != ""); reason != "" {
		return delegateResumability{Reason: reason}
	}
	// Native worktree tools spec §9 step 5 — two independent revival defenses:
	// (1) the disposed flag (set when this delegate's isolation lane was
	// removed at the creator session's close) is a fast, explicit refusal; and
	// (2) an unconditional WorkingDir stat is the crash net that also covers
	// out-of-band deletion of ANY delegate's working directory. Disposed is
	// checked first so a properly disposed lane reports the clearer reason even
	// though its directory is also gone.
	if rec.Disposed {
		return delegateResumability{Reason: notResumableWorktreeDisposed}
	}
	desc := rec.DelegateRestore
	if wd := strings.TrimSpace(desc.WorkingDir); wd != "" {
		if _, err := os.Stat(wd); err != nil {
			return delegateResumability{Reason: notResumableWorkingDirMissing}
		}
	}
	childID := strings.TrimSpace(desc.ChildSessionID)

	meta, err := schema.LoadSessionMeta(s.stateDir, childID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return delegateResumability{Reason: notResumableMissingChildSessionMeta}
		}
		return delegateResumability{Reason: notResumableCorruptChildSessionMeta}
	}
	if strings.TrimSpace(meta.ID) != childID {
		return delegateResumability{Reason: notResumableCorruptChildSessionMeta}
	}

	transcriptPath := filepath.Join(s.stateDir, sessionsSubdir, childID+".transcript.jsonl")
	if _, err := os.Stat(transcriptPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return delegateResumability{Reason: notResumableMissingChildTranscript}
		}
		return delegateResumability{Reason: notResumableCorruptChildTranscript}
	}
	var transcriptData transcriptData
	if mode == delegateResumabilityPreflight {
		transcriptData, err = readStrictChildTranscript(transcriptPath, childID, s.strictTranscriptMaxLineBytes)
	} else {
		header, validateErr := validateStrictChildTranscript(transcriptPath, childID, s.strictTranscriptMaxLineBytes)
		transcriptData.Header = header
		err = validateErr
	}
	if err != nil {
		if errors.Is(err, errStrictChildTranscriptSessionMismatch) {
			return delegateResumability{Reason: notResumableTranscriptSessionMismatch}
		}
		return delegateResumability{Reason: notResumableCorruptChildTranscript}
	}

	if sub := s.subagents.get(childID); sub != nil {
		sub.mu.Lock()
		running := sub.running
		sub.mu.Unlock()
		if running {
			return delegateResumability{Reason: notResumableChildSessionBusy}
		}
	}

	profile, err := s.resolveDelegateRestoreProfile(meta, desc)
	if err != nil {
		return delegateResumability{Reason: notResumableProfileUnavailable}
	}
	result := delegateResumability{Resumable: true}
	if mode == delegateResumabilityPreflight {
		result.Preflight = &delegateRestorePreflight{
			Meta:       meta,
			Transcript: transcriptData,
			History:    s.resumeHistoryForRestore(transcriptData.Entries),
			Profile:    profile,
		}
	}
	return result
}

func (s *Session) resolveDelegateRestoreProfile(meta schema.SessionMeta, desc *jobstore.DelegateRestoreDescriptor) (*provider.Profile, error) {
	if s == nil {
		return nil, errors.New("profile unavailable")
	}
	base := s.currentProfile()
	if base == nil {
		return nil, errors.New("profile unavailable")
	}
	if desc != nil {
		profileID := strings.TrimSpace(desc.ResolvedProfileID)
		model := strings.TrimSpace(desc.ResolvedModel)
		if profileID != "" && model != "" {
			return s.resolveDelegateRestoreProfileRef(base, profileID, model)
		}
		return nil, errors.New("delegate restore descriptor missing resolved model")
	}
	return nil, errors.New("delegate restore descriptor missing resolved profile")
}

func (s *Session) resolveDelegateRestoreProfileRef(base *provider.Profile, profileID, model string) (*provider.Profile, error) {
	ref := profileID + "/" + model
	if s.resolveProfile != nil {
		resolved, err := s.resolveProfile(ref)
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			return nil, fmt.Errorf("profile %q unavailable", ref)
		}
		if resolved.ID() != base.ID() {
			resolved = resolved.WithCommunicateOverridesFrom(base)
		}
		return resolved, nil
	}
	if profileID != base.ID() {
		return nil, fmt.Errorf("profile %q unavailable", ref)
	}
	return base.WithModel(model), nil
}

func (s *Session) restoreTerminalDelegateChild(rec *jobstore.JobRecord, childID string, preflight *delegateRestorePreflight) (*subagent, error) {
	if s == nil || s.subagents == nil || rec == nil {
		return nil, errors.New("delegate session restore unavailable")
	}
	if preflight == nil {
		return nil, errors.New("strict delegate restore preflight is required")
	}
	if !isRuntimeLostDelegate(rec) && (rec.Resumable == nil || !*rec.Resumable) {
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
	existing, pending, started, err := s.subagents.beginReconstruction(childID)
	if err != nil {
		return nil, err
	}
	if !started {
		if existing != nil {
			return existing, nil
		}
		return pending.wait()
	}
	var tracked *subagent
	var restoreErr error
	defer func() {
		s.subagents.finishReconstruction(childID, pending, tracked, restoreErr)
	}()
	tracked, restoreErr = s.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	return tracked, restoreErr
}

func (s *Session) restoreTerminalDelegateChildClaimed(rec *jobstore.JobRecord, childID string, preflight *delegateRestorePreflight) (*subagent, error) {
	if s.delegateRestoreAfterClaim != nil {
		s.delegateRestoreAfterClaim()
	}
	desc := rec.DelegateRestore
	meta := preflight.Meta
	profile := preflight.Profile
	resumeHistory := preflight.History
	if err := s.validateRestoredDelegateRequiredTools(desc); err != nil {
		return nil, err
	}
	resultSchema := delegateResultSchemaMap(desc.ResultSchema)
	if len(resultSchema) > 0 {
		profile = provider.WithCommunicateOutputSchema(profile, resultSchema)
	}
	childEnv, err := s.restoreDelegateChildEnvironment(desc, rec.DelegateID)
	if err != nil {
		return nil, err
	}
	activatedSkillBodies, err := restoreFrozenSkillBodies(desc.FrozenSkillNames, desc.FrozenSkillBodies)
	if err != nil {
		return nil, err
	}
	restoreCfg := RestoreSessionConfig{
		StateDir:       s.stateDir,
		ResolveProfile: s.resolveProfile,
		ModelFallbacks: append([]string(nil), s.cfg.ModelFallbacks...),
		LLMRetryPolicy: s.cfg.LLMRetryPolicy,
		LLMSleep:       s.cfg.LLMSleep,
		spawn: spawnConfig{
			parentSessionID:         desc.ParentSessionID,
			parentToolCallID:        desc.OriginToolCallID,
			parentJobID:             desc.ParentJobID,
			parentDelegateID:        rec.DelegateID,
			forwardJobEvent:         s.jobManager.forwardEvent,
			parentSteer:             s.SteerWithProvenance,
			parentSteerDelivered:    s.trySteerWithProvenanceAndNotify,
			parentWatchGranted:      desc.ParentWatchGranted,
			parentInstallWatch:      restoredParentInstallWatch(s, desc),
			parentClearWatch:        restoredParentClearWatch(s, desc),
			parentGrantedJobRead:    s.lookupGrantedJobRead,
			subagentTask:            desc.Task,
			depth:                   s.depth + 1,
			delegationAllowance:     desc.DelegationAllowance,
			treeCounter:             s.treeCounter,
			rolePromptOverride:      desc.FrozenRolePrompt,
			activatedSkillBodies:    activatedSkillBodies,
			allowedToolNames:        restoredDelegateAllowedTools(desc),
			isolation:               desc.Isolation,
			communicateOutputSchema: cloneMap(resultSchema),
		},
		resumeHistory:           resumeHistory,
		deferRestoreSideEffects: true,
	}
	if strings.TrimSpace(desc.ReasoningEffort) != "" {
		meta.Config.ReasoningEffort = desc.ReasoningEffort
	}
	child, err := RestoreSessionFromMetaWithConfig(s.client, profile, childEnv, meta, restoreCfg)
	if err != nil {
		return nil, err
	}
	if err := validateRestoredDelegateTools(child, desc); err != nil {
		child.discardRestoredCandidate()
		return nil, err
	}
	now := s.sclock().Now()
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
	if s.delegateRestoreBeforeTrack != nil {
		s.delegateRestoreBeforeTrack()
	}
	tracked, inserted, err := s.subagents.trackIfAbsent(sub)
	if err != nil {
		child.discardRestoredCandidate()
		return nil, err
	}
	if !inserted {
		child.discardRestoredCandidate()
		if tracked == nil || tracked.sess == nil {
			return nil, errors.New("delegate session collision with unavailable retained runtime")
		}
		return tracked, nil
	}
	if s.delegateRestoreBeforeSideEffects != nil {
		s.delegateRestoreBeforeSideEffects(child)
	}
	endSideEffects, err := s.subagents.beginReconstructionSideEffects(childID, sub)
	if err != nil {
		child.discardRestoredCandidate()
		return nil, err
	}
	defer endSideEffects()
	if err := child.runDeferredRestoreSideEffects(); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("restored delegate side effects incomplete for %s: %v", childID, err)})
	}
	return tracked, nil
}

func (s *Session) restoreDelegateChildEnvironment(desc *jobstore.DelegateRestoreDescriptor, delegateID string) (execenv.ExecutionEnvironment, error) {
	if s == nil {
		return nil, errors.New("execution environment is not configured")
	}
	env := s.currentEnv()
	if env == nil {
		return nil, errors.New("execution environment is not configured")
	}
	policy, ok := delegateRestoreLocalEnvPolicy(desc)
	if !ok {
		return nil, errors.New("invalid delegate restore local_env_policy")
	}
	workDir, ok := delegateRestoreWorkingDir(desc)
	if !ok {
		return nil, errors.New("invalid delegate restore working_dir")
	}
	if strings.TrimSpace(desc.Isolation) == "worktree" {
		// §7 revival re-lock: a kept (unlocked) lane re-takes its serf:dlg:
		// lock before the child resumes; a foreign lock (someone switched in
		// meanwhile) refuses the restore instead of co-occupying. A lane
		// still locked with our own dlg marker (the common case — the lock
		// is held for the delegate's whole lifetime) is a no-op adopt.
		if err := s.reacquireDelegateWorktreeLock(workDir, delegateID); err != nil {
			return nil, err
		}
	}
	childEnv := env
	if le, ok := env.(*execenv.LocalExecutionEnvironment); ok {
		clone := le.WithWorkingDirectory(workDir)
		clone.EnvPolicy = policy
		childEnv = clone
	} else if workDir != env.WorkingDirectory() {
		return nil, errors.New("execution environment does not support restored working_dir")
	}
	return childEnv, nil
}

// reacquireDelegateWorktreeLock implements the native worktree tools spec §7
// revival re-lock rule for an isolated delegate lane: unlocked -> lock with
// the delegate's serf:dlg: marker (a KEPT lane after disposal — §9 lifecycle
// step 4); own dlg marker -> adopt (a literal re-lock is fatal on git; this is
// the common case, since the lock is normally held for the delegate's whole
// lifetime and disposal has not run); anything else (a session marker, a
// different delegate's marker, or an unparseable lock) -> refuse, because
// someone switched into the lane while it was unlocked (§9 Guards: "refuses
// if someone has switched in meanwhile") — never co-occupy a live delegate
// resume the way a plain session restore's ActWarnCoOccupy does.
func (s *Session) reacquireDelegateWorktreeLock(lanePath, delegateID string) error {
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return errors.New("delegate isolation worktree revival requires a local execution environment")
	}
	rootedAtLane := local.WithWorkingDirectory(lanePath)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtLane, lanePath)
	if mainRoot == "" {
		return fmt.Errorf("delegate isolation worktree %s is no longer part of a git repository", lanePath)
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	run := gitRunner(context.Background(), controlEnv)
	locked, reason, lsErr := lockStateOf(run, lanePath)
	if lsErr != nil {
		return fmt.Errorf("delegate isolation worktree %s lock state could not be verified: %w", lanePath, lsErr)
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, delegateID)
	}
	switch worktree.Decide(worktree.EvDelegateRevive, st) {
	case worktree.ActLock:
		marker := worktree.FormatDelegateMarker(delegateID, s.id)
		if _, err := run("worktree", "lock", "--reason", marker, lanePath); err != nil {
			return fmt.Errorf("failed to re-lock delegate isolation worktree %s: %w", lanePath, err)
		}
		return nil
	case worktree.ActAdopt:
		return nil
	default:
		occupant := reason
		if occupant == "" {
			occupant = "an unknown owner"
		}
		return fmt.Errorf("delegate isolation worktree %s is locked by %s; refusing to revive into it", lanePath, occupant)
	}
}

func hasValidDelegateRestoreLocalEnvPolicy(desc *jobstore.DelegateRestoreDescriptor) bool {
	_, ok := delegateRestoreLocalEnvPolicy(desc)
	return ok
}

func hasValidDelegateRestoreWorkingDir(desc *jobstore.DelegateRestoreDescriptor) bool {
	_, ok := delegateRestoreWorkingDir(desc)
	return ok
}

func delegateRestoreLocalEnvPolicy(desc *jobstore.DelegateRestoreDescriptor) (execenv.EnvVarPolicy, bool) {
	if desc == nil {
		return execenv.EnvPolicyDefault, false
	}
	return localEnvPolicyFromName(desc.LocalEnvPolicy)
}

func delegateRestoreWorkingDir(desc *jobstore.DelegateRestoreDescriptor) (string, bool) {
	if desc == nil {
		return "", false
	}
	workDir := strings.TrimSpace(desc.WorkingDir)
	if workDir == "" {
		return "", false
	}
	return workDir, true
}

func restoredDelegateAllowedTools(desc *jobstore.DelegateRestoreDescriptor) []string {
	if desc == nil || len(desc.FrozenToolNames) == 0 {
		return nil
	}
	if len(desc.FrozenToolNames) == 1 && desc.FrozenToolNames[0] == "*" {
		return nil
	}
	return appendUniqueStrings(append([]string(nil), desc.FrozenToolNames...), desc.ExplicitToolGrants...)
}

func validateRestoredDelegateTools(child *Session, desc *jobstore.DelegateRestoreDescriptor) error {
	required := restoredDelegateRequiredTools(desc)
	if len(required) == 0 {
		return nil
	}
	if child == nil || child.reg == nil {
		return errors.New("restored delegate tool registry unavailable")
	}
	return validateRestoredDelegateRequiredToolNames(child.reg.RegisteredNames(), required)
}

func (s *Session) validateRestoredDelegateRequiredTools(desc *jobstore.DelegateRestoreDescriptor) error {
	required := restoredDelegateRequiredTools(desc)
	if len(required) == 0 {
		return nil
	}
	if s == nil || s.reg == nil {
		return errors.New("restored delegate tool registry unavailable")
	}
	registered := s.reg.RegisteredNames()
	if desc.DelegationAllowance <= 0 {
		for _, name := range rootOnlySubagentTools() {
			if desc.ParentWatchGranted && name == "job_watch" {
				continue
			}
			delete(registered, name)
		}
	}
	return validateRestoredDelegateRequiredToolNames(registered, required)
}

func restoredParentInstallWatch(s *Session, desc *jobstore.DelegateRestoreDescriptor) func(observerSessionID string, observerDelegateID string, args watchArgs) (watchResult, error) {
	if desc == nil || !desc.ParentWatchGranted {
		return nil
	}
	return s.installParentSourceWatchForChild
}

func restoredParentClearWatch(s *Session, desc *jobstore.DelegateRestoreDescriptor) func(observerSessionID string, observerDelegateID string, watchID string) (watchResult, error) {
	if desc == nil || !desc.ParentWatchGranted {
		return nil
	}
	return s.clearParentSourceWatchForChild
}

func validateRestoredDelegateRequiredToolNames(registered map[string]bool, required []string) error {
	var missing []string
	for _, name := range required {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("restored delegate required tool(s) unavailable: %s", strings.Join(missing, ", "))
}

func restoredDelegateRequiredTools(desc *jobstore.DelegateRestoreDescriptor) []string {
	if desc == nil {
		return nil
	}
	var required []string
	if len(desc.FrozenToolNames) != 1 || desc.FrozenToolNames[0] != "*" {
		required = append(required, desc.FrozenToolNames...)
	}
	required = appendUniqueStrings(required, desc.ExplicitToolGrants...)
	return compactToolNames(required)
}

func compactToolNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func delegateResultSchemaMap(schema any) map[string]any {
	if schema == nil {
		return nil
	}
	if m, ok := schema.(map[string]any); ok {
		if len(m) == 0 {
			return nil
		}
		return cloneMap(m)
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

func (s *Session) sendRunningDelegateMessage(target, message string, rec *jobstore.JobRecord, fromWatch bool, p *provenance.Causal) sendMessageResult {
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
	// A mid-drive child (sub.driving) is in flight just like a running one: steer
	// the message into the single in-flight (drive) turn rather than reject it
	// (spec §3, A7 steer-into-drive decision).
	driving := sub.driving
	running := sub.running || driving
	if !running {
		sub.mu.Unlock()
		return sendMessageFailed(target, fmt.Errorf("not_controllable: delegate job %q is running but session %q is not live", target, childID))
	}
	if fromWatch && run == nil && !driving {
		// A FromWatch send to a sub.running child whose runtime job record vanished
		// is a genuine "running but runtime not live" inconsistency — reject it. A
		// DRIVING child is the legitimate run==nil case: the drive turn mints no
		// jm.running entry, so we steer into it via trySteer below instead of
		// hard-failing (spec §3, A7) — a hard failure here would make the live drain
		// path permanently dropWatchSend the frame (watchSendHardFailure).
		sub.mu.Unlock()
		return sendMessageFailed(target, fmt.Errorf("not_controllable: delegate job %q is running but runtime job is not live", target))
	}
	steerProvenance := provenance.Clone(p)
	if steerProvenance == nil {
		steerProvenance = s.activeCausalProvenance()
	}
	delivered := sub.sess.trySteerWithProvenance(message, steerProvenance)
	if !delivered {
		sub.mu.Unlock()
		if fromWatch {
			return sendMessageResult{
				Target:                    target,
				DelegateID:                rec.DelegateID,
				JobID:                     rec.JobID,
				LatestJobID:               rec.JobID,
				Type:                      string(jobstore.JobDelegate),
				Status:                    jobstore.StatusRunning,
				RunningInBackground:       true,
				Action:                    "steered",
				Delivered:                 false,
				WatchSendDeliveryClass:    watchSendBusy,
				WatchSendDeliveryClassSet: true,
			}
		}
		return sendMessageFailed(target, fmt.Errorf("not_controllable: delegate job %q is running but session is not accepting messages", target))
	}
	if fromWatch {
		// A driving child has no jm.running entry (run==nil): the drive turn already
		// owns the steered frame, so there is no runtime job to tag fromWatch on.
		if run != nil {
			run.fromWatch.Store(true)
		}
		sub.runFromWatch = true
	}
	sub.mu.Unlock()
	return sendMessageResult{
		Target:              target,
		DelegateID:          rec.DelegateID,
		JobID:               rec.JobID,
		LatestJobID:         rec.JobID,
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusRunning,
		RunningInBackground: true,
		Action:              "steered",
		TranscriptRef:       rec.TranscriptRef,
	}
}

// delegateControlOwnedBySession is the shared control-surface predicate for
// delegate_send and job_list. Empty owner metadata is local to the current store;
// a non-empty mismatch is a descendant-owned forwarded copy.
func delegateControlOwnedBySession(ownerSessionID, sessionID string) bool {
	return ownerSessionID == "" || ownerSessionID == sessionID
}

func (s *Session) resumeOrFindRunningDelegate(jm *jobManager, childID, message string, sub *subagent, transcriptRef, delegateID string, resultSchema any, restore *jobstore.DelegateRestoreDescriptor, fromWatch bool, watchProvenance *provenance.Causal) (*runningJob, <-chan error, *jobstore.JobRecord, error) {
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
	resumeTime := s.sclock().Now()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		sub.mu.Unlock()
		runCancel()
		return nil, nil, nil, errors.New("session is closed")
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()

	run, err := s.attachDelegateJobFromWatchWithDelegate(jm, childID, message, sub, delegateID, resultSchema, restore, fromWatch, watchProvenance)
	if err != nil {
		sub.mu.Unlock()
		runCancel()
		s.sendersWG.Done()
		return nil, nil, nil, err
	}
	relinkDelegateChildToJob(sub.sess, run.rec.JobID)
	resetSubagentForRunLockedFromWatch(sub, runCancel, resumeTime, fromWatch)
	done := sub.done
	sub.mu.Unlock()

	finalizeErr := s.bridgeDelegateFinalizationWithDone(run.rec.JobID, childID, sub, done, true)
	s.launchSubagentRun(runCtx, sub, runCancel, message, watchProvenance)
	return run, finalizeErr, nil, nil
}

func relinkDelegateChildToJob(child *Session, jobID string) {
	if child == nil {
		return
	}
	child.cfg.spawn.parentJobID = jobID
	if child.jobManager != nil {
		child.jobManager.setParentJobID(jobID)
	}
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

func (s *Session) hasCallerRoute() bool {
	return s != nil && (s.cfg.spawn.parentSteerDelivered != nil || s.cfg.spawn.parentSteer != nil)
}

func delegateJobRecordForJobID(jm *jobManager, jobID string) (*jobstore.JobRecord, bool) {
	rec, err := findJobRecord(jm, jobID)
	if err != nil || rec == nil || strings.TrimSpace(rec.DelegateID) == "" {
		return nil, false
	}
	return rec, true
}

func waitForDelegateFinalization(ctx context.Context, s *Session, jm *jobManager, run *runningJob, finalizeErr <-chan error) delegateResult {
	timer := jm.clock.NewTimer(delegateFinalizeWaitTimeout)
	defer timer.Stop()

	select {
	case <-run.done:
		return delegateTerminalResult(s, jm, run)
	case err := <-finalizeErr:
		if err != nil {
			return delegateFinalizeFailedResult(run, "finalize_failed", err)
		}
		return delegateTerminalResult(s, jm, run)
	case <-ctx.Done():
		return delegateFinalizeFailedResult(run, "cancelled", ctx.Err())
	case <-timer.C():
		return delegateFinalizeFailedResult(run, "finalize_timeout", errors.New("delegate finalization timed out"))
	}
}

func waitForResumedDelegateResult(ctx context.Context, s *Session, jm *jobManager, target, resumedFromJobID string, run *runningJob, finalizeErr <-chan error, blockTimeoutMS int) sendMessageResult {
	blockTimeout := time.Duration(clampShellBlockTimeoutMS(blockTimeoutMS)) * time.Millisecond
	timer := jm.clock.NewTimer(blockTimeout)
	defer timer.Stop()

	var res delegateResult
	select {
	case <-run.done:
		res = delegateTerminalResult(s, jm, run)
	case err := <-finalizeErr:
		if err != nil {
			res = delegateFinalizeFailedResult(run, "finalize_failed", err)
			break
		}
		res = delegateTerminalResult(s, jm, run)
	case <-timer.C():
		output, _, truncated, readErr := tailOutput(run.output, shellInlineOutputBytes)
		return sendMessageResult{
			Target:              target,
			DelegateID:          run.rec.DelegateID,
			StartedJobID:        run.rec.JobID,
			JobID:               run.rec.JobID,
			LatestJobID:         run.rec.JobID,
			Type:                string(jobstore.JobDelegate),
			Status:              jobstore.StatusRunning,
			Reason:              "foreground_timeout",
			RunningInBackground: true,
			TimedOut:            true,
			Action:              "started",
			ResumedFromJobID:    resumedFromJobID,
			TranscriptRef:       run.rec.TranscriptRef,
			Output:              output,
			Truncated:           truncated,
			Err:                 readErr,
		}
	case <-ctx.Done():
		res = delegateFinalizeFailedResult(run, "cancelled", ctx.Err())
	}
	return sendMessageResultFromDelegateResult(target, resumedFromJobID, "started", res)
}

func sendMessageResultFromDelegateResult(target, resumedFromJobID, action string, res delegateResult) sendMessageResult {
	return sendMessageResult{
		Target:                   target,
		DelegateID:               res.DelegateID,
		StartedJobID:             res.StartedJobID,
		JobID:                    res.JobID,
		LatestJobID:              res.LatestJobID,
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
		Watching:                 res.Watching,
		Watches:                  res.Watches,
		Worktree:                 res.Worktree,
		Err:                      res.Err,
	}
}

func delegateFinalizeFailedResult(run *runningJob, reason string, err error) delegateResult {
	return delegateResult{
		DelegateID:          run.rec.DelegateID,
		StartedJobID:        run.rec.JobID,
		JobID:               run.rec.JobID,
		LatestJobID:         run.rec.JobID,
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusFailed,
		Reason:              reason,
		RunningInBackground: true,
		TranscriptRef:       run.rec.TranscriptRef,
		Err:                 err,
	}
}

// validGrantRange renders the inclusive set a caller with `own` allowance may grant
// to a child (always strictly less than its own): "0" at the floor, else "0..N-1".
func validGrantRange(own int) string {
	if own <= 1 {
		return "0"
	}
	return fmt.Sprintf("0..%d", own-1)
}

// validateDelegateGrant is the pure grant decision lifted out of startDelegate:
// a session may grant a child an allowance strictly less than its own (spec §1).
// It folds the accept predicate with the human-facing range renderer so the
// wrapper only reads its own allowance under lock and constructs the error.
func validateDelegateGrant(requested, own int) (ok bool, validRange string) {
	return requested < own, validGrantRange(own)
}

func delegateStartFailed(err error) delegateResult {
	return delegateResult{
		Type:   string(jobstore.JobDelegate),
		Status: jobstore.StatusFailed,
		Reason: "start_failed",
		Err:    err,
	}
}

func delegateStartFailedWithIDs(delegateID, jobID string, err error) delegateResult {
	res := delegateStartFailed(err)
	res.DelegateID = delegateID
	res.StartedJobID = jobID
	res.JobID = jobID
	res.LatestJobID = jobID
	return res
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

func delegateDone(sub *subagent) <-chan struct{} {
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	return done
}

func (s *Session) bridgeDelegateFinalization(jobID, childID string, sub *subagent, armNotification bool) (<-chan error, <-chan struct{}) {
	done := delegateDone(sub)
	return s.bridgeDelegateFinalizationWithDone(jobID, childID, sub, done, armNotification), done
}

func (s *Session) bridgeDelegateFinalizationWithDone(jobID, childID string, sub *subagent, done <-chan struct{}, armNotification bool) <-chan error {
	finalizeErr := make(chan error, 1)
	go func() {
		<-done
		if armNotification {
			finalizeErr <- s.finalizeDelegate(jobID, childID, sub)
			return
		}
		finalizeErr <- s.finalizeDelegateNoNotification(jobID, childID, sub)
	}()
	return finalizeErr
}

func (s *Session) attachDelegateJob(jm *jobManager, childID, task string, sub *subagent) (*runningJob, error) {
	link := delegateJobLink{delegateID: jobstore.NewDelegateID(), generation: jobstore.NewDelegateGeneration(), create: true}
	return s.attachDelegateJobWithRestoreAndDelegate(jm, childID, task, sub, jobstore.NewJobID(), nil, false, nil, nil, link, nil)
}

//nolint:unused // retained for watch restore paths that create a fresh delegate without a preexisting delegate id.
func (s *Session) attachDelegateJobFromWatch(jm *jobManager, childID, task string, sub *subagent, resultSchema any, restore *jobstore.DelegateRestoreDescriptor, fromWatch bool) (*runningJob, error) {
	link := delegateJobLink{delegateID: jobstore.NewDelegateID(), generation: jobstore.NewDelegateGeneration(), create: true}
	return s.attachDelegateJobWithRestoreAndDelegate(jm, childID, task, sub, jobstore.NewJobID(), resultSchema, fromWatch, nil, restore, link, nil)
}

func (s *Session) attachDelegateJobFromWatchWithDelegate(jm *jobManager, childID, task string, sub *subagent, delegateID string, resultSchema any, restore *jobstore.DelegateRestoreDescriptor, fromWatch bool, watchProvenance *provenance.Causal) (*runningJob, error) {
	link := delegateJobLink{delegateID: delegateID, generation: jobstore.NewDelegateGeneration()}
	return s.attachDelegateJobWithRestoreAndDelegate(jm, childID, task, sub, jobstore.NewJobID(), resultSchema, fromWatch, nil, restore, link, watchProvenance)
}

func (s *Session) attachDelegateJobWithID(jm *jobManager, childID, task string, sub *subagent, jobID string, resultSchema any, fromWatch bool) (*runningJob, error) {
	link := delegateJobLink{delegateID: jobstore.NewDelegateID(), generation: jobstore.NewDelegateGeneration(), create: true}
	return s.attachDelegateJobWithRestoreAndDelegate(jm, childID, task, sub, jobID, resultSchema, fromWatch, nil, nil, link, nil)
}

func (s *Session) attachDelegateJobWithPrepared(jm *jobManager, childID, task string, sub *subagent, jobID string, resultSchema any, fromWatch bool, prepared *preparedSubagentRun) (*runningJob, error) {
	link := delegateJobLink{delegateID: jobstore.NewDelegateID(), generation: jobstore.NewDelegateGeneration(), create: true}
	return s.attachDelegateJobWithRestoreAndDelegate(jm, childID, task, sub, jobID, resultSchema, fromWatch, prepared, nil, link, nil)
}

func (s *Session) attachDelegateJobWithPreparedAndDelegate(jm *jobManager, childID, task string, sub *subagent, jobID, delegateID, delegateGeneration, agentType string, resultSchema any, fromWatch bool, prepared *preparedSubagentRun) (*runningJob, error) {
	link := delegateJobLink{delegateID: delegateID, generation: delegateGeneration, agentType: agentType, create: true}
	return s.attachDelegateJobWithRestoreAndDelegate(jm, childID, task, sub, jobID, resultSchema, fromWatch, prepared, nil, link, nil)
}

//nolint:unused // retained for crash-restore attach paths that resume from a previous delegate descriptor.
func (s *Session) attachDelegateJobWithRestore(jm *jobManager, childID, task string, sub *subagent, jobID string, resultSchema any, fromWatch bool, prepared *preparedSubagentRun, previousRestore *jobstore.DelegateRestoreDescriptor) (*runningJob, error) {
	return s.attachDelegateJobWithRestoreAndDelegate(jm, childID, task, sub, jobID, resultSchema, fromWatch, prepared, previousRestore, delegateJobLink{}, nil)
}

type delegateJobLink struct {
	delegateID string
	generation string
	agentType  string
	create     bool
}

func (s *Session) attachDelegateJobWithRestoreAndDelegate(jm *jobManager, childID, task string, sub *subagent, jobID string, resultSchema any, fromWatch bool, prepared *preparedSubagentRun, previousRestore *jobstore.DelegateRestoreDescriptor, link delegateJobLink, jobProvenance *provenance.Causal) (*runningJob, error) {
	// Claim the tree-counter slot for this running delegate turn (spec §4). A
	// fresh spawn already reserved in prepareSubagentRun — transfer that slot. A
	// resume (prepared == nil) reserves a new slot here and surfaces
	// tree_at_capacity to the resuming tool call. The slot is owned by the
	// runningJob once it enters jm.running; until then every error path below
	// releases it so a failed attach never leaks a slot.
	var treeSlot *treeReservation
	if prepared != nil {
		treeSlot = prepared.treeSlot
		prepared.treeSlot = nil
	} else {
		slot, ok := s.reserveTreeSlot()
		if !ok {
			return nil, errTreeAtCapacity
		}
		treeSlot = slot
	}

	startedAt := jm.now()
	transcriptRef := encodeRef("", childID)
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	output, err := jobstore.OpenOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		treeSlot.release()
		return nil, err
	}
	restore := s.delegateRestoreDescriptor(jobID, childID, task, transcriptRef, resultSchema, prepared)
	if previousRestore != nil {
		restore = s.resumedDelegateRestoreDescriptor(jobID, childID, transcriptRef, resultSchema, previousRestore)
	}
	// The observer delegate's job provenance attributes the run to the watch
	// delivery driving it. The driving watch (threaded from the watch send) wins;
	// the parent's active provenance is next; the previous restore descriptor's
	// provenance is the fallback only when neither is present — i.e. crash
	// reconstruction, where active provenance is empty. This keeps a cross-watch
	// resume attributed to the current watch instead of re-pinning the observer to
	// the watch that first drove it.
	if jobProvenance == nil {
		jobProvenance = s.activeCausalProvenance()
	}
	if jobProvenance == nil && previousRestore != nil {
		jobProvenance = previousRestore.Provenance
	}
	restore.Provenance = provenance.Clone(jobProvenance)
	parentJobID := jm.currentParentJobID()
	run := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			Status:           jobstore.StatusRunning,
			Task:             task,
			OwnerSessionID:   s.id,
			VisibleToSession: s.id,
			ParentJobID:      parentJobID,
			DelegateID:       link.delegateID,
			OriginItemID:     restore.OriginItemID,
			TranscriptRef:    transcriptRef,
			DelegateRestore:  restore,
			StartedAt:        startedAt,
			Phase:            jobPhaseStarting,
			LastActivity:     &startedAt,
			OutputPath:       outputPath,
			Provenance:       provenance.Clone(jobProvenance),
		},
		output:         output,
		signal:         func() { cancelDelegateSub(sub) },
		done:           make(chan struct{}),
		durableStarted: true,
		treeSlot:       treeSlot,
	}
	run.fromWatch.Store(fromWatch)

	jm.mu.Lock()
	if jm.closing {
		jm.mu.Unlock()
		_ = output.Close()
		_ = os.Remove(outputPath)
		treeSlot.release()
		return nil, errJobManagerClosing
	}
	var startEvents []jobstore.Event
	if link.delegateID != "" && link.generation != "" {
		created := jobstore.Event{
			Kind:       jobstore.EventDelegateCreated,
			TS:         startedAt,
			DelegateID: link.delegateID,
			Delegate: &jobstore.DelegateEvent{
				ChildSessionID:   childID,
				TranscriptRef:    transcriptRef,
				OwnerSessionID:   s.id,
				VisibleSessionID: s.id,
				AgentType:        link.agentType,
				Generation:       link.generation,
				Resumable:        true,
			},
		}
		if !link.create {
			created.Delegate = &jobstore.DelegateEvent{
				ChildSessionID:   childID,
				TranscriptRef:    transcriptRef,
				OwnerSessionID:   s.id,
				VisibleSessionID: s.id,
				Generation:       link.generation,
				Resumable:        true,
			}
		}
		startEvents = append(startEvents, created)
	}
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            run.rec.JobID,
		Type:             run.rec.Type,
		Task:             run.rec.Task,
		OwnerSessionID:   run.rec.OwnerSessionID,
		VisibleToSession: run.rec.VisibleToSession,
		ParentJobID:      run.rec.ParentJobID,
		DelegateID:       run.rec.DelegateID,
		OriginItemID:     run.rec.OriginItemID,
		StartedAt:        &startedAt,
		OutputPath:       run.rec.OutputPath,
		TranscriptRef:    run.rec.TranscriptRef,
		DelegateRestore:  run.rec.DelegateRestore,
		Provenance:       provenance.Clone(run.rec.Provenance),
	}
	startEvents = append(startEvents, started)
	if err := jm.appendJobEvents(startEvents); err != nil {
		jm.mu.Unlock()
		_ = output.Close()
		_ = os.Remove(outputPath)
		treeSlot.release()
		return nil, err
	}
	if err := jm.forwardLocked(started); err != nil {
		_ = output.Close()
		treeSlot.release()
		if terminalErr := jm.appendStartForwardFailure(run.rec.JobID, output, run.rec.Provenance); terminalErr != nil {
			// Double-fault: the start forward failed AND the durable
			// forward_failed terminal could not be appended. Unlike the shell
			// path, there is no delegate analog to finalizeShellUntilDurable to
			// thread here (the run is never added to jm.running, so no
			// finalizer can adopt it). The job_started event is left without a
			// terminal; the owner's next restart reconciles it to
			// stopped/runtime_lost via the standard restart-reconciliation
			// path. Building a finalizer for this rare double-fault
			// (local-store append failing twice) is unwarranted.
			jm.mu.Unlock()
			return nil, errors.Join(errDelegateStartForwardTerminalFailed, err, terminalErr)
		}
		jm.mu.Unlock()
		return nil, errors.Join(errDelegateStartForwardFailed, err)
	}
	run.watchdogStop = make(chan struct{})
	watchdogStop := run.watchdogStop
	jm.running[run.rec.JobID] = run
	jm.mu.Unlock()
	jm.emitJobStarted(started, run)
	jm.startQuietWatchdog(run.rec.JobID, watchdogStop)
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
			desc.DelegationAllowance = sub.sess.delegationAllowance
		}
		return desc
	}
	desc.ParentSessionID = prepared.parentSessionID
	desc.ParentJobID = prepared.parentJobID
	desc.OriginToolCallID = prepared.originToolCallID
	desc.OriginItemID = prepared.originItemID
	desc.Task = prepared.task
	desc.AgentType = prepared.agentType
	desc.RequestedModel = prepared.requestedModel
	desc.ReasoningEffort = prepared.reasoningEffort
	desc.AgentName = prepared.resolvedAgentName
	desc.FrozenRolePrompt = prepared.frozenRolePrompt
	desc.FrozenTaskPrompt = prepared.frozenTaskPrompt
	desc.FrozenToolNames = append([]string(nil), prepared.frozenToolNames...)
	desc.FrozenSkillNames = append([]string(nil), prepared.frozenSkillNames...)
	desc.FrozenSkillBodies = append([]string(nil), prepared.frozenSkillBodies...)
	desc.WorkingDir = prepared.workingDir
	desc.LocalEnvPolicy = prepared.localEnvPolicy
	desc.Isolation = prepared.isolation
	desc.ExplicitToolGrants = append([]string(nil), prepared.explicitToolGrants...)
	if prepared.resultSchema != nil {
		desc.ResultSchema = cloneDelegateResultSchema(prepared.resultSchema)
	}
	if prepared.sub != nil && prepared.sub.sess != nil {
		profile := prepared.sub.sess.currentProfile()
		desc.ResolvedProfileID = profile.ID()
		desc.ResolvedModel = profile.Model()
		desc.DelegationAllowance = prepared.sub.sess.delegationAllowance
		desc.ParentWatchGranted = prepared.sub.sess.cfg.spawn.parentWatchGranted
	}
	return desc
}

func (s *Session) resumedDelegateRestoreDescriptor(jobID, childID, transcriptRef string, resultSchema any, previous *jobstore.DelegateRestoreDescriptor) *jobstore.DelegateRestoreDescriptor {
	version := previous.Version
	if version == 0 {
		version = 1
	}
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:             version,
		ChildSessionID:      childID,
		TranscriptRef:       transcriptRef,
		ParentSessionID:     s.id,
		ParentJobID:         jobID,
		OwnerSessionID:      s.id,
		VisibleSessionID:    s.id,
		OriginTurnID:        previous.OriginTurnID,
		OriginToolCallID:    previous.OriginToolCallID,
		OriginItemID:        previous.OriginItemID,
		Task:                previous.Task,
		AgentType:           previous.AgentType,
		RequestedModel:      previous.RequestedModel,
		ResolvedProfileID:   previous.ResolvedProfileID,
		ResolvedModel:       previous.ResolvedModel,
		ReasoningEffort:     previous.ReasoningEffort,
		AgentName:           previous.AgentName,
		FrozenRolePrompt:    previous.FrozenRolePrompt,
		FrozenTaskPrompt:    previous.FrozenTaskPrompt,
		FrozenToolNames:     append([]string(nil), previous.FrozenToolNames...),
		FrozenSkillNames:    append([]string(nil), previous.FrozenSkillNames...),
		FrozenSkillBodies:   append([]string(nil), previous.FrozenSkillBodies...),
		WorkingDir:          previous.WorkingDir,
		LocalEnvPolicy:      previous.LocalEnvPolicy,
		ResultSchema:        cloneDelegateResultSchema(previous.ResultSchema),
		ExplicitToolGrants:  append([]string(nil), previous.ExplicitToolGrants...),
		DelegationAllowance: previous.DelegationAllowance,
		ParentWatchGranted:  previous.ParentWatchGranted,
		Isolation:           previous.Isolation,
		Provenance:          provenance.Clone(previous.Provenance),
	}
	if resultSchema != nil {
		desc.ResultSchema = cloneDelegateResultSchema(resultSchema)
	}
	return desc
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
	return s.finalizeDelegateWithNotification(jobID, childID, sub, true)
}

func (s *Session) finalizeDelegateNoNotification(jobID, childID string, sub *subagent) error {
	return s.finalizeDelegateWithNotification(jobID, childID, sub, false)
}

func (s *Session) finalizeDelegateWithNotification(jobID, childID string, sub *subagent, armNotification bool) error {
	jm, err := sessionJobManager(s)
	if err != nil {
		return err
	}
	if sub == nil {
		sub = s.subagents.get(childID)
	}

	for {
		err := s.finalizeDelegateOnce(jm, jobID, sub, armNotification)
		if err == nil {
			return nil
		}
		if delegateFinalizeStopsRetry(jm, err) {
			jm.abandonRunningJob(jobID)
			return err
		}
		jm.clock.Sleep(delegateFinalizeRetryDelay)
	}
}

func (s *Session) finalizeDelegateOnce(jm *jobManager, jobID string, sub *subagent, armNotification bool) error {
	prepare := func(run *runningJob) (jobstore.Status, string, *int, error) {
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
		runProvenance := provenance.Clone(sub.runProvenance)
		sub.mu.Unlock()

		var structured any
		structuredCaptureFailed := false
		if childSess != nil {
			structured = childSess.CommunicateStructured()
		} else if delegateResultSchema(run.rec) != nil {
			structuredCaptureFailed = true
		}
		finalProvenance := provenance.Union(run.rec.Provenance, runProvenance)
		jm.mu.Lock()
		if jm.running[run.rec.JobID] == run {
			run.rec.Provenance = provenance.Clone(finalProvenance)
		}
		jm.mu.Unlock()
		outputProvenance := provenance.Clone(finalProvenance)

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
					if _, err := appendDelegateOutput(jm, run, nil, outputProvenance); err != nil {
						return "", "", nil, err
					}
				}
				jm.mu.Lock()
				run.delegateOutputAppended = true
				jm.mu.Unlock()
				break
			}
			n, err := appendDelegateOutput(jm, run, output[outputWritten:], outputProvenance)
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

		if err := s.persistDelegateResumability(jm, run); err != nil {
			return "", "", nil, err
		}

		jobStatus, reason := delegateTerminalStatus(jm, run, status)
		return jobStatus, reason, nil, nil
	}
	if armNotification {
		return jm.finalizeWithRun(jobID, prepare)
	}
	return jm.finalizeWithRunNoNotification(jobID, prepare)
}

func (s *Session) persistDelegateResumability(jm *jobManager, run *runningJob) error {
	if s == nil || jm == nil || run == nil || run.rec == nil || run.rec.Type != jobstore.JobDelegate {
		return nil
	}
	jm.mu.Lock()
	if run.delegateResumeAssessed {
		jm.mu.Unlock()
		return nil
	}
	rec := cloneJobRecord(run.rec)
	jm.mu.Unlock()

	assessment := s.assessDelegateResumability(rec, delegateResumabilityProjection)
	resumable := assessment.Resumable
	event := jobstore.Event{
		Kind:          jobstore.EventJobSessionAssigned,
		TS:            jm.now(),
		JobID:         rec.JobID,
		TranscriptRef: rec.TranscriptRef,
		Resumable:     &resumable,
	}
	if !assessment.Resumable {
		event.NotResumableWhy = assessment.Reason
	}
	if err := jm.appendEvent(event); err != nil {
		return err
	}

	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run {
		run.delegateResumeAssessed = true
		run.rec.Resumable = &resumable
		run.rec.NotResumableWhy = event.NotResumableWhy
	}
	jm.mu.Unlock()
	return nil
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

func appendDelegateOutput(jm *jobManager, run *runningJob, b []byte, p *provenance.Causal) (int, error) {
	if run == nil || run.output == nil {
		return 0, nil
	}
	return jm.appendJobOutputWithProvenance(run.rec.JobID, run.output, b, p)
}

func delegateTerminalStatus(jm *jobManager, run *runningJob, status SubagentStatus) (jobstore.Status, string) {
	var stopStatus jobstore.Status
	var stopReason string
	if jm != nil && run != nil {
		jm.mu.Lock()
		stopStatus, stopReason = run.stopStatus, run.stopReason
		jm.mu.Unlock()
	}
	return resolveDelegateTerminalStatus(stopStatus, stopReason, status)
}

// resolveDelegateTerminalStatus is the pure terminal-status decision lifted out
// of delegateTerminalStatus: a parent-recorded stop status always overrides the
// child's own outcome, otherwise the child's SubagentStatus maps to a terminal
// job Status. The wrapper reads run.stopStatus/run.stopReason under lock.
func resolveDelegateTerminalStatus(stopStatus jobstore.Status, stopReason string, child SubagentStatus) (jobstore.Status, string) {
	if stopStatus != "" {
		return stopStatus, stopReason
	}
	switch child {
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

func delegateTerminalResult(s *Session, jm *jobManager, run *runningJob) delegateResult {
	rec, err := findJobRecord(jm, run.rec.JobID)
	if err != nil {
		return delegateResult{
			DelegateID:    run.rec.DelegateID,
			StartedJobID:  run.rec.JobID,
			JobID:         run.rec.JobID,
			LatestJobID:   run.rec.JobID,
			Type:          string(jobstore.JobDelegate),
			Status:        jobstore.StatusFailed,
			Reason:        "read_failed",
			TranscriptRef: run.rec.TranscriptRef,
			Err:           err,
		}
	}
	output, _, truncated, err := jm.readOutput(rec.JobID, shellInlineOutputBytes)
	activeWatches := activeDelegateWatchSummaries(jm, rec)
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
	var wt *delegateWorktreeReport
	if rec.Status.IsTerminal() {
		wt = s.isolatedDelegateWorktreeReport(rec.DelegateRestore)
	}
	return delegateResult{
		DelegateID:               rec.DelegateID,
		StartedJobID:             rec.JobID,
		JobID:                    rec.JobID,
		LatestJobID:              rec.JobID,
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
		Watching:                 len(activeWatches) > 0,
		Watches:                  activeWatches,
		Worktree:                 wt,
		Err:                      err,
	}
}

// isolatedDelegateWorktreeReport computes the native worktree tools spec §9
// lifecycle step 3 fields (path, branch, commits-ahead-of-base, dirty state)
// for a terminal job belonging to an isolation delegate, by inspecting the
// lane directly through the parent's control env. Returns nil when desc is
// not an isolation delegate's descriptor, or the lane cannot be inspected
// (its sidecar or the worktree itself is gone, or git fails) — a broken
// partial report is omitted rather than surfaced.
func (s *Session) isolatedDelegateWorktreeReport(desc *jobstore.DelegateRestoreDescriptor) *delegateWorktreeReport {
	if s == nil || desc == nil || strings.TrimSpace(desc.Isolation) != "worktree" {
		return nil
	}
	lanePath := strings.TrimSpace(desc.WorkingDir)
	if lanePath == "" {
		return nil
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return nil
	}
	rootedAtLane := local.WithWorkingDirectory(lanePath)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtLane, lanePath)
	if mainRoot == "" {
		return nil
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	run := gitRunner(context.Background(), controlEnv)

	// The sidecar (written at lane creation, see createDelegateWorktree) is
	// the authoritative source of the lane's branch and base SHA — the
	// worktree name has no "/" (it is the delegate id), so its metaDir is a
	// sibling of the lane's parent dir, same as worktreeCreate's own layout.
	metaDir := filepath.Join(filepath.Dir(lanePath), ".meta")
	sc, err := worktree.ReadSidecar(metaDir, filepath.Base(lanePath))
	if err != nil {
		return nil
	}
	clean, _, err := worktree.CleanTree(run, lanePath)
	if err != nil {
		return nil
	}
	aheadOut, err := run("-C", lanePath, "rev-list", "--count", sc.BaseSHA+"..HEAD")
	if err != nil {
		return nil
	}
	ahead, convErr := strconv.Atoi(strings.TrimSpace(aheadOut))
	if convErr != nil {
		return nil
	}
	return &delegateWorktreeReport{
		Path:   lanePath,
		Branch: sc.Branch,
		Ahead:  ahead,
		Dirty:  !clean,
	}
}

func activeDelegateWatchSummaries(jm *jobManager, rec *jobstore.JobRecord) []watchListEntry {
	if jm == nil || rec == nil || rec.DelegateID == "" || rec.TranscriptRef == "" {
		return nil
	}
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil || childID == "" {
		return nil
	}
	return jm.liveWatchSummariesForReceiver(childID, rec.DelegateID)
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
	if m, ok := schema.(map[string]any); ok && len(m) == 0 {
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
	if m, ok := cloned.(map[string]any); ok && len(m) == 0 {
		return nil
	}
	return cloned
}
