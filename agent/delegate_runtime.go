package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

const (
	notResumableMissingDelegateResumeMetadata = "missing_delegate_resume_metadata"
	notResumableParentLinkageUnavailable      = "parent_linkage_unavailable"
	notResumableMissingChildSessionMeta       = "missing_child_session_meta"
	notResumableCorruptChildSessionMeta       = "corrupt_child_session_meta"
	notResumableMissingChildTranscript        = "missing_child_transcript"
	notResumableCorruptChildTranscript        = "corrupt_child_transcript"
	notResumableTranscriptSessionMismatch     = "transcript_session_mismatch"
	notResumableWorkingDirMissing             = "working_dir_missing"
)

type delegateRuntime struct {
	owner *Session
}

type stableDelegateSendOutcome struct {
	result sendMessageResult
	commit *delegateToolResultCommit
}

type delegateRunLeaseContextKey struct{}

type delegatePreseededInput struct {
	sessionID string
	input     string
}

type delegatePreseededInputContextKey struct{}

type delegateChildSessionIDContextKey struct{}

type delegateAttentionRunContextKey struct{}

type delegateIsolation struct {
	env             execenv.ExecutionEnvironment
	ownsFreshEnv    bool
	worktreePath    string
	worktreeProject identifier.Project
}

type delegateQuietAttentionClaim struct {
	token       uint64
	lease       delegateLease
	sequence    uint64
	activityAt  time.Time
	attentionID string
	content     string
	receiver    *Session
	done        chan struct{}
}

func (c *delegateTreeController) ReportActivity(lease delegateLease, at time.Time) error {
	if c == nil {
		return errDelegateStaleLease
	}
	if at.IsZero() {
		at = c.now()
	}
	c.mu.Lock()
	aggregate, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if at.Before(live.activityAt) {
		c.mu.Unlock()
		return nil
	}
	rearm := live.quietNotified || live.quietClaim != nil && live.quietClaim.sequence == live.quietSequence
	if at.Equal(live.activityAt) && !rearm {
		c.mu.Unlock()
		return nil
	}
	if rearm {
		live.quietSequence++
		live.quietNotified = false
	}
	live.activityAt = at
	c.evidenceVersion++
	plan := c.capturedPlanLocked(aggregate.DelegateID)
	c.mu.Unlock()
	c.emitDelegateUpdate(plan)
	return nil
}

func (s *Session) runDelegateQuietWatchdogTick(lease delegateLease, now time.Time) error {
	if s == nil || s.delegateController == nil {
		return errDelegateDeliveryReceiverUnavailable
	}
	claim, err := s.delegateController.BeginQuietAttention(s, lease, now)
	if err != nil || claim == nil {
		return err
	}
	deferred, appendErr := s.appendQuietAttentionAtTurnBoundary(claim.attentionID, claim.content)
	if deferred {
		return s.delegateController.CompleteQuietAttention(claim, false)
	}
	completionErr := s.delegateController.CompleteQuietAttention(claim, appendErr == nil)
	if appendErr == nil && completionErr == nil {
		completionErr = s.armDelegateAttention(claim.attentionID)
	}
	return errors.Join(appendErr, completionErr)
}

func (s *Session) startDelegateQuietWatchdog(ctx context.Context, lease delegateLease) context.CancelFunc {
	if ctx == nil {
		ctx = context.Background()
	}
	watchCtx, cancel := context.WithCancel(ctx)
	ticker := s.sclock().NewTicker(delegateQuietCheckInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C():
				_ = s.runDelegateQuietWatchdogTick(lease, now)
			case <-watchCtx.Done():
				return
			}
		}
	}()
	return func() {
		ticker.Stop()
		cancel()
	}
}

func delegateQuietAttentionID(lease delegateLease) string {
	return delegateQuietAttentionIDForStretch(lease, 1)
}

func delegateQuietAttentionIDForStretch(lease delegateLease, sequence uint64) string {
	return fmt.Sprintf("quiet:%s:%d:%d", lease.delegateID, lease.generation, sequence)
}

func delegateQuietAttentionContent(lease delegateLease, activityAt time.Time) string {
	return fmt.Sprintf(
		"<delegate-notification delegate_id=%q>%s</delegate-notification>",
		html.EscapeString(lease.delegateID),
		html.EscapeString(quietWatchdogMessage(delegateQuietWindow, activityAt)),
	)
}

func (c *delegateTreeController) BeginQuietAttention(receiver *Session, lease delegateLease, now time.Time) (*delegateQuietAttentionClaim, error) {
	if c == nil || receiver == nil {
		return nil, errDelegateDeliveryReceiverUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		return nil, err
	}
	if c.hasSettlementClaimLocked(lease) {
		return nil, errDelegateTargetBusy
	}
	expectedReceiver := c.rootRuntime
	if aggregate.Descriptor.ParentDelegateID != "" {
		parent := c.live[aggregate.Descriptor.ParentDelegateID]
		if parent == nil {
			return nil, errDelegateDeliveryReceiverUnavailable
		}
		expectedReceiver = parent.runtime
	}
	if expectedReceiver == nil || expectedReceiver != receiver {
		return nil, errDelegateDeliveryReceiverUnavailable
	}
	activityAt := live.activityAt
	if activityAt.IsZero() {
		activityAt = aggregate.RunStartedAt
	}
	if now.IsZero() {
		now = c.now()
	}
	if activityAt.IsZero() || now.Before(activityAt.Add(delegateQuietWindow)) || live.quietNotified || live.quietClaim != nil {
		return nil, nil
	}
	if live.quietSequence == 0 {
		live.quietSequence = 1
	}
	c.nextToken++
	claim := &delegateQuietAttentionClaim{
		token:       c.nextToken,
		lease:       lease,
		sequence:    live.quietSequence,
		activityAt:  activityAt,
		attentionID: delegateQuietAttentionIDForStretch(lease, live.quietSequence),
		content:     delegateQuietAttentionContent(lease, activityAt),
		receiver:    receiver,
		done:        make(chan struct{}),
	}
	live.quietClaim = claim
	c.quietClaims[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

func (c *delegateTreeController) CompleteQuietAttention(claim *delegateQuietAttentionClaim, committed bool) error {
	if c == nil || claim == nil {
		return errDelegateStaleLease
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quietClaims[claim.token] != claim || claim.receiver == nil {
		return errDelegateStaleLease
	}
	delete(c.quietClaims, claim.token)
	live := c.live[claim.lease.delegateID]
	if live != nil && live.quietClaim == claim {
		live.quietClaim = nil
	}
	if c.stop != nil {
		if _, tracked := c.stop.quietClaims[claim.token]; tracked {
			delete(c.stop.quietClaims, claim.token)
			c.signalStopProgressLocked()
		}
	}
	var result error
	if committed {
		aggregate := c.durable[claim.lease.delegateID]
		if aggregate == nil || aggregate.Generation != claim.lease.generation || !aggregate.CurrentRunOpen || aggregate.Phase != delegatestore.PhaseRunning || live == nil || live.binding == nil || live.binding.lease != claim.lease || live.quietSequence != claim.sequence || !live.activityAt.Equal(claim.activityAt) {
			result = errDelegateStaleLease
		} else {
			live.quietNotified = true
		}
	}
	c.evidenceVersion++
	close(claim.done)
	return result
}

func (s *Session) appendQuietAttentionAtTurnBoundary(attentionID, content string) (bool, error) {
	s.delegateDeliveryMu.Lock()
	defer s.delegateDeliveryMu.Unlock()
	s.mu.Lock()
	processing := s.state == SessionProcessing
	closed := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closed {
		return false, errors.New("delegate attention receiver is closed")
	}
	if processing {
		return true, nil
	}
	_, err := s.appendDelegateNotificationDurably(attentionID, content)
	return false, err
}

func bindStableDelegateActivity(child *Session, controller *delegateTreeController, lease delegateLease) {
	if child == nil || controller == nil {
		return
	}
	owner := controller.stableDelegateOwnerRuntime(lease)
	bindStableDelegateActivityToOwner(child, controller, lease, owner)
}

func bindStableDelegateActivityToOwner(child *Session, controller *delegateTreeController, lease delegateLease, owner *Session) {
	if child == nil || controller == nil {
		return
	}
	var forward func(jobstore.Event) error
	if owner != nil && owner.jobManager != nil {
		forward = owner.jobManager.forwardEvent
	}
	child.mu.Lock()
	child.cfg.spawn.parentDelegateID = lease.delegateID
	child.cfg.spawn.forwardJobEvent = forward
	child.cfg.spawn.parentJobActivity = func(string, string) {
		_ = controller.ReportActivity(lease, child.sclock().Now())
	}
	jm := child.jobManager
	child.mu.Unlock()
	if jm != nil {
		jm.bindStableDelegateParent(controller, lease, forward, child)
	}
}

func (s *Session) driveStableDelegateAttention(sub *subagent) bool {
	if s == nil || sub == nil || sub.sess == nil || sub.sess.owningDelegateID == "" || s.delegateController == nil || sub.sess.delegateController != s.delegateController {
		return false
	}
	var ids []string
	if retryID := s.delegateController.reservedAttentionID(sub.sess); retryID != "" {
		ids = []string{retryID}
	} else {
		var err error
		ids, err = sub.sess.pendingDelegateAttentionIDs()
		if err != nil {
			s.emit(events.EventWarning, warningDataFromError("inspect delegate attention", err))
			return true
		}
	}
	if len(ids) == 0 {
		return false
	}
	sub.mu.Lock()
	blocked := sub.closed || sub.running || sub.driving || sub.disposeGated || sub.fatalRunGated || sub.finalizing
	sub.mu.Unlock()
	if blocked {
		return true
	}
	s.mu.Lock()
	closed := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closed {
		return true
	}
	reservation, err := s.delegateController.ReserveAttention(sub.sess, ids[0])
	if err != nil {
		if !errors.Is(err, errDelegateTargetBusy) && !errors.Is(err, errTreeAtCapacity) {
			s.emit(events.EventWarning, warningDataFromError("reserve delegate attention", err))
		}
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	if err := sub.sess.acceptDelegateAttention(reservation); err != nil {
		s.emit(events.EventWarning, warningDataFromError("accept delegate attention", err))
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	started, err := s.delegateController.CommitStart(reservation)
	if err != nil {
		s.emit(events.EventWarning, warningDataFromError("commit delegate attention", err))
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	s.delegateController.emitDelegateUpdate(started.plan)
	if launchErr := s.launchAcceptedDelegateAttention(sub, started); launchErr != nil {
		plans, finishErr := s.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(launchErr, "launch_failed"))
		if executeErr := s.executeDelegateMutationPlans(plans); finishErr == nil {
			finishErr = executeErr
		}
		s.emit(events.EventWarning, warningDataFromError("launch delegate attention", errors.Join(launchErr, finishErr)))
	}
	return true
}

func (s *Session) launchAcceptedDelegateAttention(sub *subagent, started delegateStartCommit) error {
	if s == nil || sub == nil || sub.sess == nil || s.delegateController == nil {
		return errDelegateDeliveryReceiverUnavailable
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("delegate attention owner is closed")
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()
	runCtx, runCancel := context.WithCancel(started.ctx)
	runCtx = context.WithValue(runCtx, delegateRunLeaseContextKey{}, started.lease)
	runCtx = context.WithValue(runCtx, delegateAttentionRunContextKey{}, struct{}{})
	sub.mu.Lock()
	sub.fatalRunGated = false
	resetSubagentForRunLocked(sub, runCancel, started.startedAt)
	sub.mu.Unlock()
	bindStableDelegateActivity(sub.sess, s.delegateController, started.lease)
	s.launchSubagentRun(runCtx, sub, runCancel, "", descriptorProvenance(started.descriptor))
	s.startDelegateQuietWatchdog(started.ctx, started.lease)
	return nil
}

func (s *Session) restoreColdDelegateAttentionRuntime(delegateID string) (*Session, *subagent, error) {
	if s == nil || s.delegateController == nil || delegateID == "" {
		return nil, nil, errors.New("cold delegate attention restore identity is incomplete")
	}
	started, parentID, err := s.delegateController.idleDelegateRestoreCommit(delegateID)
	if err != nil {
		return nil, nil, err
	}
	owner, err := s.restoreColdDelegateOwnerRuntime(parentID)
	if err != nil {
		return nil, nil, err
	}

	runtime := delegateRuntime{owner: owner}
	sub, restored, finishRestore, err := runtime.restoreIdleForSend(started)
	if err != nil {
		finishRestore(nil, err)
		return nil, nil, err
	}
	if sub == nil || sub.sess == nil {
		err = errors.New("cold delegate attention runtime is unavailable")
		finishRestore(nil, err)
		return nil, nil, err
	}
	if restored {
		candidate := sub
		tracked, inserted, trackErr := owner.subagents.admitReconstructed(candidate, func(selected *subagent) error {
			return s.delegateController.AttachIdleRuntime(delegateID, selected.sess)
		})
		if trackErr != nil {
			candidate.sess.discardRestoredCandidate()
			finishRestore(nil, trackErr)
			return nil, nil, trackErr
		}
		if !inserted {
			candidate.sess.discardRestoredCandidate()
			sub = tracked
			restored = false
		}
	} else if err := s.delegateController.AttachIdleRuntime(delegateID, sub.sess); err != nil {
		finishRestore(nil, err)
		return nil, nil, err
	}
	if restored && owner.delegateRestoreBeforeSideEffects != nil {
		owner.delegateRestoreBeforeSideEffects(sub.sess)
	}
	bindStableDelegateActivityToOwner(sub.sess, s.delegateController, started.lease, owner)
	if restored {
		if err := sub.sess.runDeferredRestoreSideEffects(); err != nil {
			sub.sess.emit(events.EventWarning, warningDataFromError("restored delegate side effects incomplete", err))
		}
	}
	finishRestore(sub, nil)
	return owner, sub, nil
}

func (s *Session) restoreColdDelegateOwnerRuntime(parentID string) (*Session, error) {
	if parentID == "" {
		return s, nil
	}
	if resident := s.delegateController.residentDelegateRuntime(parentID); resident != nil {
		return resident, nil
	}
	_, parent, err := s.restoreColdDelegateAttentionRuntime(parentID)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.sess == nil {
		return nil, errors.New("cold delegate attention parent runtime is unavailable")
	}
	return parent.sess, nil
}

func (s *Session) admitOwedDelegateAttentionStarts() error {
	if s == nil || s.delegateController == nil {
		return errors.New("owed delegate attention controller is unavailable")
	}
	owed, err := s.delegateController.owedAttentionStartsFromTranscripts()
	if err != nil {
		return err
	}
	for _, start := range owed {
		if err := s.admitOwedDelegateAttentionStart(start); err != nil {
			return fmt.Errorf("delegate %s attention %s: %w", start.delegateID, start.attentionID, err)
		}
	}
	return nil
}

func (s *Session) admitOwedDelegateAttentionStart(owed delegateOwedAttentionStart) error {
	owner, err := s.restoreColdDelegateOwnerRuntime(owed.parentID)
	if err != nil {
		return fmt.Errorf("restore owner runtime: %w", err)
	}
	reservation, err := s.delegateController.reserveOwedAttentionStart(owed.delegateID, owed.attentionID, owed.generation, owed.pendingIDs)
	if err != nil {
		return fmt.Errorf("reserve owed generation: %w", err)
	}
	started, err := s.delegateController.commitStart(reservation)
	if err != nil {
		return fmt.Errorf("commit owed generation: %w", err)
	}
	runtime := delegateRuntime{owner: owner}
	sub, restored, finishRestore, restoreErr := runtime.restoreIdleForSend(started)
	if restoreErr != nil {
		finishRestore(nil, restoreErr)
		if failErr := s.failOwedDelegateAttentionStart(started, restoreErr); failErr != nil {
			return fmt.Errorf("restore owed runtime: %w", failErr)
		}
		return nil
	}
	if sub == nil || sub.sess == nil {
		cause := errors.New("owed delegate attention runtime is unavailable")
		finishRestore(nil, cause)
		if failErr := s.failOwedDelegateAttentionStart(started, cause); failErr != nil {
			return fmt.Errorf("restore owed runtime: %w", failErr)
		}
		return nil
	}
	if restored {
		candidate := sub
		tracked, inserted, trackErr := owner.subagents.admitReconstructed(candidate, func(selected *subagent) error {
			return s.delegateController.attachRuntime(started.lease, selected.sess)
		})
		if trackErr != nil {
			candidate.sess.discardRestoredCandidate()
			finishRestore(nil, trackErr)
			if failErr := s.failOwedDelegateAttentionStart(started, trackErr); failErr != nil {
				return fmt.Errorf("track owed runtime: %w", failErr)
			}
			return nil
		}
		if !inserted {
			candidate.sess.discardRestoredCandidate()
			sub = tracked
			restored = false
		}
	} else if err := s.delegateController.attachRuntime(started.lease, sub.sess); err != nil {
		finishRestore(nil, err)
		if failErr := s.failOwedDelegateAttentionStart(started, err); failErr != nil {
			return fmt.Errorf("attach owed runtime: %w", failErr)
		}
		return nil
	}
	if restored && owner.delegateRestoreBeforeSideEffects != nil {
		owner.delegateRestoreBeforeSideEffects(sub.sess)
	}
	bindStableDelegateActivityToOwner(sub.sess, s.delegateController, started.lease, owner)
	if restored {
		if err := sub.sess.runDeferredRestoreSideEffects(); err != nil {
			sub.sess.emit(events.EventWarning, warningDataFromError("restored delegate side effects incomplete", err))
		}
	}
	finishRestore(sub, nil)
	if err := owner.launchAcceptedDelegateAttention(sub, started); err != nil {
		if failErr := s.failOwedDelegateAttentionStart(started, err); failErr != nil {
			return fmt.Errorf("launch owed runtime: %w", failErr)
		}
		return nil
	}
	return nil
}

func (s *Session) failOwedDelegateAttentionStart(started delegateStartCommit, cause error) error {
	plans, finishErr := s.delegateController.failCommittedRestart(started.lease, delegatePermanentStartFailure(cause, "construction_failed"))
	if finishErr != nil {
		return errors.Join(cause, finishErr)
	}
	return errors.Join(cause, s.executeDelegateMutationPlans(plans))
}

// escalateUnreachableDelegateAttention transfers pending attention wakes that
// a permanently closed ancestor fences off forever to the root session -- each
// message under its own original identity and content, mirroring the cold
// repair in repairPermanentlyUnreachableDelegateAttention. Transfer before
// resolution: a crash in between replays into an idempotent no-op append and a
// second resolution attempt, never a silently discarded message. A failing
// delegate does not starve the rest of the batch.
func (s *Session) escalateUnreachableDelegateAttention() bool {
	progressed := false
	var failures []error
	for _, plan := range s.delegateController.permanentlyFencedDelegateAttention() {
		if err := s.escalateOneUnreachableDelegateAttention(plan); err != nil {
			failures = append(failures, fmt.Errorf("delegate %s: %w", plan.delegateID, err))
			continue
		}
		progressed = true
	}
	if len(failures) != 0 {
		s.emit(events.EventWarning, warningDataFromError("escalate unreachable delegate attention", errors.Join(failures...)))
		s.scheduleStableDelegateAttentionRetry()
	}
	return progressed
}

func (s *Session) escalateOneUnreachableDelegateAttention(plan delegateFencedAttentionEscalation) error {
	sourcePath, sourceSessionID, err := delegateTranscriptPathFromRef(s.delegateController.stateDir, plan.transcriptRef)
	if err != nil {
		return err
	}
	fold, err := readDelegateAttentionFold(sourcePath, sourceSessionID)
	if err != nil {
		return err
	}
	for _, attentionID := range plan.attentionIDs {
		message, exists := fold.content[attentionID]
		if _, resolved := fold.resolutions[attentionID]; resolved || !exists {
			// Already durably resolved (or never durable): only the stale
			// process-local wake remains.
			s.delegateController.forgetDelegateAttention(plan.delegateID, attentionID)
			continue
		}
		if _, err := s.appendDelegateAttentionMessageDurably(attentionID, message); err != nil {
			return err
		}
		if err := s.armDelegateAttention(attentionID); err != nil {
			return err
		}
		if err := s.delegateController.resolveDelegateAttentionDurably(plan.runtime, plan.transcriptRef, []string{attentionID}, delegateAttentionDiscarded); err != nil {
			return err
		}
		s.delegateController.forgetDelegateAttention(plan.delegateID, attentionID)
	}
	return nil
}

func (s *Session) drivePendingStableDelegateAttention() bool {
	if s == nil || s.delegateController == nil || !s.isRootDelegateAttentionReceiver() {
		return false
	}
	escalated := s.escalateUnreachableDelegateAttention()
	delegateID, _, pending := s.delegateController.nextIdleDelegateAttention()
	if !pending {
		return escalated
	}
	owner, sub, err := s.restoreColdDelegateAttentionRuntime(delegateID)
	if err != nil {
		s.emit(events.EventWarning, warningDataFromError("restore delegate attention", err))
		s.scheduleStableDelegateAttentionRetry()
		return true
	}
	owner.driveStableDelegateAttention(sub)
	if s.delegateController.hasPendingDelegateAttention() {
		s.scheduleStableDelegateAttentionRetry()
	} else {
		s.resetStableDelegateAttentionRetry()
	}
	return true
}

func (c *delegateTreeController) stableDelegateOwnerRuntime(lease delegateLease) *Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, _, err := c.exactLeaseLocked(lease)
	if err != nil {
		return nil
	}
	if aggregate.Descriptor.ParentDelegateID == "" {
		return c.rootRuntime
	}
	parent := c.live[aggregate.Descriptor.ParentDelegateID]
	if parent == nil {
		return nil
	}
	return parent.runtime
}

func delegateInputWasPreseeded(ctx context.Context, sessionID, input string) bool {
	preseeded, ok := ctx.Value(delegatePreseededInputContextKey{}).(delegatePreseededInput)
	return ok && preseeded.sessionID == sessionID && preseeded.input == input
}

func (s *Session) createDelegate(ctx context.Context, args delegateArgs) delegateResult {
	return (delegateRuntime{owner: s}).create(ctx, args)
}

func (runtime delegateRuntime) send(ctx context.Context, delegateID, message string, maxWaitMS int) stableDelegateSendOutcome {
	s := runtime.owner
	failed := func(err error) stableDelegateSendOutcome {
		return stableDelegateSendOutcome{result: sendMessageFailed(delegateID, err)}
	}
	if s == nil || s.delegateController == nil {
		return failed(errors.New("delegate controller is unavailable"))
	}
	if delegateID == "" || message == "" {
		return failed(errors.New("invalid_request: delegate_id and message are required"))
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return failed(err)
	}
	if plans, steerErr := s.delegateController.Steer(ctx, actor, delegateID, message); steerErr == nil {
		_ = s.executeDelegateMutationPlans(plans)
		return stableDelegateSendOutcome{result: sendMessageResult{
			Target:              delegateID,
			DelegateID:          delegateID,
			Type:                delegateResourceType,
			Status:              jobstore.StatusRunning,
			RunningInBackground: true,
			Action:              "steered",
		}}
	} else if !errors.Is(steerErr, errDelegateTargetBusy) {
		return failed(steerErr)
	}
	reservation, err := s.delegateController.ReserveStart(actor, delegateID)
	if err != nil {
		return failed(err)
	}
	var waiter *delegateInlineWaiter
	if maxWaitMS > 0 {
		waiter, err = s.delegateController.RegisterInlineWaiter(reservation)
		if err != nil {
			_ = s.delegateController.AbortStart(reservation)
			return failed(err)
		}
	}
	started, err := s.delegateController.CommitStart(reservation)
	if err != nil {
		return failed(err)
	}
	sub, restored, finishRestore, err := runtime.restoreIdleForSend(started)
	if err != nil {
		return runtime.failStableSendStartAfterDispatch(ctx, started, delegateID, waiter, maxWaitMS, err, func() {
			finishRestore(nil, err)
		})
	}
	if restored {
		candidate := sub
		tracked, inserted, trackErr := s.subagents.admitReconstructed(candidate, func(selected *subagent) error {
			return s.delegateController.AttachRuntime(started.lease, selected.sess)
		})
		if trackErr != nil {
			candidate.sess.discardRestoredCandidate()
			return runtime.failStableSendStartAfterDispatch(ctx, started, delegateID, waiter, maxWaitMS, trackErr, func() {
				finishRestore(nil, trackErr)
			})
		}
		if !inserted {
			candidate.sess.discardRestoredCandidate()
			sub = tracked
			restored = false
		}
		if s.delegateRestoreBeforeSideEffects != nil {
			s.delegateRestoreBeforeSideEffects(sub.sess)
		}
	}
	if sub == nil || sub.sess == nil {
		cause := errors.New("delegate runtime is unavailable")
		return runtime.failStableSendStartAfterDispatch(ctx, started, delegateID, waiter, maxWaitMS, cause, func() {
			finishRestore(nil, cause)
		})
	}
	if !restored {
		if err := s.delegateController.AttachRuntime(started.lease, sub.sess); err != nil {
			return runtime.failStableSendStartAfterDispatch(ctx, started, delegateID, waiter, maxWaitMS, err, func() {
				finishRestore(nil, err)
			})
		}
	}
	bindStableDelegateActivity(sub.sess, s.delegateController, started.lease)
	if restored {
		if err := sub.sess.runDeferredRestoreSideEffects(); err != nil {
			s.emit(events.EventWarning, warningDataFromError("restored delegate side effects incomplete", err))
		}
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		cause := errors.New("session is closed")
		return runtime.failStableSendStartAfterDispatch(ctx, started, delegateID, waiter, maxWaitMS, cause, func() {
			finishRestore(sub, nil)
		})
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()
	finishRestore(sub, nil)
	claim, err := s.delegateController.BeginStartInput(started.lease)
	if err != nil {
		s.sendersWG.Done()
		return runtime.failStableSendStart(ctx, started, delegateID, waiter, maxWaitMS, err)
	}
	if err := runtime.preseedInput(sub.sess, message, started.transcriptPath); err != nil {
		plans, completeErr := s.delegateController.CompleteStartInput(claim, false, delegatePermanentStartFailure(err, "input_persist_failed"))
		s.sendersWG.Done()
		return runtime.stableSendFailureOutcome(ctx, started, waiter, maxWaitMS, plans, errors.Join(err, completeErr))
	}
	plans, err := s.delegateController.CompleteStartInput(claim, true, delegateFinish{})
	if err != nil {
		s.sendersWG.Done()
		return runtime.stableSendFailureOutcome(ctx, started, waiter, maxWaitMS, plans, err)
	}
	if err := s.executeDelegateMutationPlans(plans); err != nil {
		s.sendersWG.Done()
		failurePlans, finishErr := s.delegateController.FinishGeneration(started.lease, delegatePermanentStartFailure(err, "launch_failed"))
		if finishErr != nil {
			finishErr = errors.Join(finishErr, s.delegateController.ReportFinalizationQuiesced(started.lease, sub.sess))
		}
		return runtime.stableSendFailureOutcome(ctx, started, waiter, maxWaitMS, failurePlans, errors.Join(err, finishErr))
	}
	runCtx, runCancel := context.WithCancel(started.ctx)
	runCtx = context.WithValue(runCtx, delegateRunLeaseContextKey{}, started.lease)
	runCtx = context.WithValue(runCtx, delegatePreseededInputContextKey{}, delegatePreseededInput{sessionID: sub.sess.id, input: message})
	sub.mu.Lock()
	sub.fatalRunGated = false
	resetSubagentForRunLocked(sub, runCancel, started.startedAt)
	sub.mu.Unlock()
	s.launchSubagentRun(runCtx, sub, runCancel, message, descriptorProvenance(started.descriptor))
	s.startDelegateQuietWatchdog(started.ctx, started.lease)
	result := sendMessageResult{
		Target:              delegateID,
		DelegateID:          delegateID,
		Type:                delegateResourceType,
		Status:              jobstore.StatusRunning,
		RunningInBackground: true,
		Action:              "started",
		TranscriptRef:       started.descriptor.TranscriptRef,
	}
	if waiter == nil {
		return stableDelegateSendOutcome{result: result}
	}
	waitCtx := ctx
	if maxWaitMS > 0 {
		var cancel context.CancelFunc
		waitDuration := time.Duration(maxWaitMS) * time.Millisecond
		waitCtx, cancel = context.WithTimeout(ctx, waitDuration)
		defer cancel()
		if observe := s.cfg.testOnly.delegateInlineWaitReady; observe != nil {
			observe(waitCtx, waitDuration)
		}
	}
	resolution := s.delegateController.waitForDelegateInline(waitCtx, waiter)
	if resolution.fallback || resolution.packet == nil || resolution.commit == nil {
		result.TimedOut = true
		return stableDelegateSendOutcome{result: result}
	}
	result.RunningInBackground = false
	result.Action = "completed"
	populateStableDelegateSendResult(&result, *resolution.packet)
	return stableDelegateSendOutcome{result: result, commit: resolution.commit}
}

func populateStableDelegateSendResult(result *sendMessageResult, packet delegatestore.TerminalPacket) {
	if result == nil {
		return
	}
	finish := delegatePreparedFinish(packet)
	result.Status = stableDelegateOutcomeJobStatus(finish.outcome)
	result.Reason = finish.reason
	result.ExhaustionBudget = string(finish.exhaustionBudget)
	result.ExhaustionLimit = finish.exhaustionLimit
	if finish.exhaustionResumable != nil {
		resumable := *finish.exhaustionResumable
		result.Resumable = &resumable
	}
	if len(packet.Message) != 0 {
		_ = json.Unmarshal(packet.Message, &result.Output)
	}
	if len(packet.StructuredResult) != 0 {
		result.StructuredResult = append(json.RawMessage(nil), packet.StructuredResult...)
	}
	if len(packet.StructuredResult) != 0 || packet.StructuredResultValid != nil {
		result.StructuredResultValidSet = true
		if packet.StructuredResultValid != nil {
			result.StructuredResultValid = *packet.StructuredResultValid
		}
		result.StructuredResultReason = packet.StructuredResultReason
	}
	result.Warnings = append([]string(nil), packet.Warnings...)
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(packet.Metadata, &metadata); err == nil {
		result.Task = metadata.Task
		result.Description = metadata.Description
		result.AgentType = metadata.AgentType
		result.RequestedModel = metadata.RequestedModel
		result.ResolvedProfileID = metadata.ResolvedProfileID
		result.ResolvedModel = metadata.ResolvedModel
		result.ReasoningEffort = metadata.ReasoningEffort
		result.RunStartedAt = metadata.RunStartedAt
		result.RunEndedAt = metadata.RunEndedAt
		result.LatestActivityAt = metadata.LatestActivityAt
		if metadata.CumulativeUsage != nil {
			usage := *metadata.CumulativeUsage
			result.CumulativeUsage = &usage
		}
		if metadata.Worktree != nil {
			result.Worktree = &delegateWorktreeReport{
				Path:    metadata.Worktree.Path,
				Branch:  metadata.Worktree.Branch,
				HeadSHA: metadata.Worktree.HeadSHA,
				Ahead:   metadata.Worktree.Ahead,
				Dirty:   metadata.Worktree.Dirty,
			}
		}
	}
}

func stableDelegateOutcomeJobStatus(outcome delegatestore.OutcomeStatus) jobstore.Status {
	switch outcome {
	case delegatestore.OutcomeCompleted:
		return jobstore.StatusCompleted
	case delegatestore.OutcomeCancelled:
		return jobstore.StatusCancelled
	case delegatestore.OutcomeStopped:
		return jobstore.StatusStopped
	case delegatestore.OutcomeExhausted:
		return jobstore.StatusExhausted
	default:
		return jobstore.StatusFailed
	}
}

func descriptorProvenance(descriptor delegatestore.Descriptor) *provenance.Causal {
	return provenance.Clone(descriptor.Provenance)
}

func (runtime delegateRuntime) failStableSendStart(ctx context.Context, started delegateStartCommit, delegateID string, waiter *delegateInlineWaiter, maxWaitMS int, cause error) stableDelegateSendOutcome {
	return runtime.failStableSendStartAfterDispatch(ctx, started, delegateID, waiter, maxWaitMS, cause, nil)
}

func (runtime delegateRuntime) failStableSendStartAfterDispatch(ctx context.Context, started delegateStartCommit, delegateID string, waiter *delegateInlineWaiter, maxWaitMS int, cause error, afterDispatch func()) stableDelegateSendOutcome {
	plans, finishErr := runtime.owner.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(cause, "construction_failed"))
	return runtime.stableSendFailureOutcomeAfterDispatch(ctx, started, waiter, maxWaitMS, plans, errors.Join(cause, finishErr), afterDispatch)
}

func (runtime delegateRuntime) stableSendFailureOutcome(ctx context.Context, started delegateStartCommit, waiter *delegateInlineWaiter, maxWaitMS int, plans delegateMutationPlans, cause error) stableDelegateSendOutcome {
	return runtime.stableSendFailureOutcomeAfterDispatch(ctx, started, waiter, maxWaitMS, plans, cause, nil)
}

func (runtime delegateRuntime) stableSendFailureOutcomeAfterDispatch(ctx context.Context, started delegateStartCommit, waiter *delegateInlineWaiter, maxWaitMS int, plans delegateMutationPlans, cause error, afterDispatch func()) stableDelegateSendOutcome {
	executeErr := runtime.owner.executeDelegateMutationPlans(plans)
	if afterDispatch != nil {
		afterDispatch()
	}
	result := stableDelegateFailedSendResult(started, plans, errors.Join(cause, executeErr))
	if waiter == nil || result.Action == "recovery_required" {
		return stableDelegateSendOutcome{result: result}
	}
	waitCtx := ctx
	if maxWaitMS > 0 {
		var cancel context.CancelFunc
		waitDuration := time.Duration(maxWaitMS) * time.Millisecond
		waitCtx, cancel = context.WithTimeout(ctx, waitDuration)
		defer cancel()
		if observe := runtime.owner.cfg.testOnly.delegateInlineWaitReady; observe != nil {
			observe(waitCtx, waitDuration)
		}
	}
	resolution := runtime.owner.delegateController.waitForDelegateInline(waitCtx, waiter)
	if resolution.fallback || resolution.packet == nil || resolution.commit == nil {
		result.TimedOut = true
		return stableDelegateSendOutcome{result: result}
	}
	durableStatus := result.Status
	durableReason := result.Reason
	populateStableDelegateSendResult(&result, *resolution.packet)
	if durableReason != "" {
		result.Reason = durableReason
	}
	if durableStatus != jobstore.StatusRunning {
		result.Status = durableStatus
	}
	result.Action = "completed"
	result.RunningInBackground = false
	return stableDelegateSendOutcome{result: result, commit: resolution.commit}
}

func stableDelegateFailedSendResult(started delegateStartCommit, plans delegateMutationPlans, cause error) sendMessageResult {
	snapshot := latestDelegateMutationSnapshot(started.lease.delegateID, started.plan, plans)
	resumable := snapshot.resumable
	result := sendMessageResult{
		Target:              started.lease.delegateID,
		DelegateID:          started.lease.delegateID,
		Type:                delegateResourceType,
		Status:              jobstore.StatusRunning,
		Resumable:           &resumable,
		RunningInBackground: false,
		Action:              "recovery_required",
		TranscriptRef:       started.descriptor.TranscriptRef,
		Err:                 cause,
	}
	if snapshot.lastOutcome != nil {
		result.Status = stableDelegateOutcomeJobStatus(snapshot.lastOutcome.Status)
		result.Reason = snapshot.lastOutcome.Reason
		result.Action = "completed"
	}
	deliveryID := delegateDeliveryID(started.lease.delegateID, started.lease.generation)
	for _, delivery := range plans.deliveries {
		if delivery.delegateID == started.lease.delegateID && delivery.deliveryID == deliveryID {
			durableStatus := result.Status
			durableReason := result.Reason
			populateStableDelegateSendResult(&result, delivery.packet)
			if durableReason != "" {
				result.Reason = durableReason
			}
			if durableStatus != jobstore.StatusRunning {
				result.Status = durableStatus
			}
			result.Action = "completed"
			result.RunningInBackground = false
			break
		}
	}
	return result
}

func (runtime delegateRuntime) create(ctx context.Context, args delegateArgs) delegateResult {
	s := runtime.owner
	if s == nil || s.delegateController == nil {
		return delegateStartFailed(errors.New("delegate controller is unavailable"))
	}
	task := strings.TrimSpace(args.Task)
	if task == "" {
		return delegateStartFailed(errors.New("invalid_request: task is required"))
	}
	isolationName := strings.TrimSpace(args.Isolation)
	if isolationName != "" && isolationName != "worktree" {
		return delegateStartFailed(fmt.Errorf("invalid_request: isolation %q is not supported (expected \"worktree\")", isolationName))
	}
	if strings.TrimSpace(s.stateDir) == "" {
		return delegateStartFailed(errors.New("delegate creation requires a durable state directory"))
	}
	s.mu.Lock()
	ownAllowance := s.delegationAllowance
	s.mu.Unlock()
	if ok, validRange := validateDelegateGrant(args.DelegationAllowance, ownAllowance); !ok {
		return delegateStartFailed(fmt.Errorf("invalid_request: delegation_allowance must be less than your own allowance (%d); valid grants: %s", ownAllowance, validRange))
	}
	if err := llm.ValidateReasoningEffort(args.ReasoningEffort); err != nil {
		return delegateStartFailed(err)
	}
	selection, err := s.selectSubagentModel(ctx, args.Model, args.AgentType)
	if err != nil {
		return delegateStartFailed(err)
	}
	if selection.warning != nil {
		s.emitDiagnosticWarning(*selection.warning)
	}
	var requestedSandbox *sandbox.SandboxPolicy
	if strings.TrimSpace(args.Sandbox) != "" || args.SandboxNet != nil {
		parentMode, parentNetwork := s.parentSandboxModeNet()
		requestedSandbox, err = resolveDelegateSandboxRequest(args.Sandbox, args.SandboxNet, parentMode, parentNetwork)
		if err != nil {
			return delegateStartFailed(err)
		}
	}
	descriptor, worktreeProject, err := runtime.describe(ctx, args, task, isolationName, requestedSandbox, selection)
	if err != nil {
		return delegateStartFailed(err)
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return delegateStartFailed(err)
	}
	if err := s.reclaimDelegateRuntimeCapacity(1); err != nil {
		return delegateStartFailed(err)
	}
	// Read the overlap before this delegate is prepared or tracked, while the
	// siblings it would join are still the only running children.
	sharedWorkspaceAdvisory := s.sharedWorkspaceDelegateWarning(isolationName)
	reservation, err := s.delegateController.ReserveCreate(actor, descriptor)
	if err != nil {
		return delegateStartFailed(err)
	}
	isolation, err := runtime.prepareIsolation(ctx, reservation, worktreeProject, requestedSandbox)
	if err != nil {
		abortErr := s.delegateController.AbortStart(reservation)
		isolation.cleanup(s, reservation.delegateID)
		return delegateStartFailed(errors.Join(err, abortErr))
	}
	started, err := s.delegateController.CommitStart(reservation)
	if err != nil {
		isolation.cleanup(s, reservation.delegateID)
		return delegateStartFailed(err)
	}
	createResult := func(result delegateResult) delegateResult {
		if selection.warning != nil {
			result.Warnings = []string{selection.warning.Message}
		}
		result.Worktree = s.stableDelegateWorktreeReport(started.descriptor)
		return result
	}
	s.delegateController.emitDelegateUpdate(started.plan)
	prepared, err := runtime.construct(ctx, args, selection, started, isolation)
	if err != nil {
		return createResult(runtime.failCommittedStart(started, isolation, nil, false, err, "construction_failed"))
	}
	if err := s.delegateController.AttachRuntime(started.lease, prepared.sub.sess); err != nil {
		return createResult(runtime.failCommittedStart(started, isolation, prepared, false, err, "construction_failed"))
	}
	if err := runtime.adopt(prepared); err != nil {
		return createResult(runtime.failCommittedStart(started, isolation, prepared, true, err, "construction_failed"))
	}
	claim, err := s.delegateController.BeginStartInput(started.lease)
	if err != nil {
		return createResult(runtime.failAdoptedStart(started, isolation, prepared, err, "input_admission_failed"))
	}
	preseedErr := runtime.preseedInput(prepared.sub.sess, task, started.transcriptPath)
	if preseedErr != nil {
		finish := delegatePermanentStartFailure(preseedErr, "input_persist_failed")
		plans, completeErr := s.delegateController.CompleteStartInput(claim, false, finish)
		s.delegateController.emitDelegateUpdates(plans)
		if completeErr != nil {
			runtime.retainAdoptedWithoutLaunch(prepared)
			return createResult(stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(preseedErr, completeErr)))
		}
		runtime.retainAdoptedWithoutLaunch(prepared)
		return createResult(stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, preseedErr))
	}
	plans, err := s.delegateController.CompleteStartInput(claim, true, delegateFinish{})
	s.delegateController.emitDelegateUpdates(plans)
	if err != nil {
		runtime.retainAdoptedWithoutLaunch(prepared)
		return createResult(stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, err))
	}
	bindStableDelegateActivity(prepared.sub.sess, s.delegateController, started.lease)
	s.startDelegateQuietWatchdog(started.ctx, started.lease)
	s.launchSubagentRun(prepared.runCtx, prepared.sub, prepared.runCancel, prepared.input, started.descriptor.Provenance)
	result := createResult(stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, nil))
	// The advisory rides the launched delegate's own result only: it is
	// metadata for the caller's next isolation choice, not delegate output, not
	// an EventWarning, and not durable job state a later delegate_send replays.
	result.Warnings = appendUniqueStrings(result.Warnings, sharedWorkspaceAdvisory)
	return result
}

func (s *Session) delegateActor(ctx context.Context) (delegateActor, error) {
	if lease, ok := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease); ok {
		if s.owningDelegateID == "" || lease.delegateID != s.owningDelegateID {
			return delegateActor{}, errDelegateStaleLease
		}
		return delegateActor{lease: &lease}, nil
	}
	if s.owningDelegateID != "" {
		return delegateActor{}, errDelegateStaleLease
	}
	return rootDelegateActor(s.delegateRootSessionID), nil
}

func (runtime delegateRuntime) describe(ctx context.Context, args delegateArgs, task, isolationName string, requestedSandbox *sandbox.SandboxPolicy, selection subagentModelSelection) (delegatestore.Descriptor, identifier.Project, error) {
	s := runtime.owner
	s.mu.Lock()
	childConfig := s.cfg.toSnapshot().Clone()
	sharedTaskStoreOwnerSessionID := s.cfg.spawn.sharedTaskStoreOwnerSessionID
	s.mu.Unlock()
	agentType := strings.TrimSpace(args.AgentType)
	if agentType == "" {
		agentType = "default"
	}
	agentName, rolePrompt := stableDelegateRole(selection, args.DelegationAllowance > 0, s)
	reasoningEffort := strings.TrimSpace(args.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = strings.TrimSpace(childConfig.ReasoningEffort)
	}
	allTools, allowedTools, deniedTools := baseSubagentToolPolicy(selection.agent, args.DelegationAllowance > 0)
	if !allTools {
		allowedTools = ensureRecoveryReader(allowedTools, s.reg)
	}
	toolNameCeiling := stableDelegateToolNameCeiling(s.reg, s.resultToolName(), allTools, allowedTools, deniedTools, args.DelegationAllowance > 0, args.WatchParent, isolationName)
	var frozenSkillNames, frozenSkillBodies []string
	if selection.agent != nil {
		for _, name := range selection.agent.Skills {
			body, err := skill.ResolveSkillContent(s.skills, name)
			if err == nil && strings.TrimSpace(body) != "" {
				frozenSkillNames = append(frozenSkillNames, name)
				frozenSkillBodies = append(frozenSkillBodies, body)
			}
		}
	}
	resultSchema, err := json.Marshal(args.ResultSchema)
	if err != nil {
		return delegatestore.Descriptor{}, identifier.Project{}, fmt.Errorf("invalid result schema: %w", err)
	}
	if len(args.ResultSchema) == 0 {
		resultSchema = nil
	}
	sandboxSnapshot := stableDelegateSandboxSnapshot(requestedSandbox)
	if requestedSandbox == nil {
		if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok && local.Sandbox != nil && local.Sandbox.Enforced() {
			inherited := local.Sandbox.Inputs()
			sandboxSnapshot = stableDelegateSandboxSnapshot(&inherited)
		}
	}
	childConfig.MaxTurns = 500
	childConfig.AgentName = agentName
	childConfig.ReasoningEffort = reasoningEffort
	childConfig.MCPConfigFiles = nil
	childConfig.MCPInline = nil
	childConfig.Sandbox = ""
	childConfig.SandboxNet = nil
	if sandboxSnapshot != nil {
		childConfig.Sandbox = sandboxSnapshot.Mode
		if sandboxSnapshot.Network != nil {
			network := *sandboxSnapshot.Network
			childConfig.SandboxNet = &network
		}
	}
	if childConfig.ShareTasksWithChildren {
		if sharedTaskStoreOwnerSessionID == "" {
			sharedTaskStoreOwnerSessionID = s.id
		}
	} else {
		sharedTaskStoreOwnerSessionID = ""
	}
	descriptor := delegatestore.Descriptor{
		VisibleSessionID:              s.id,
		Task:                          task,
		Description:                   task,
		AgentType:                     agentType,
		RequestedModel:                selection.requestedModel,
		ResolvedProfileID:             selection.profile.ID(),
		ResolvedModel:                 selection.profile.Model(),
		FrozenRolePrompt:              rolePrompt,
		ToolNameCeiling:               toolNameCeiling,
		FrozenSkillNames:              frozenSkillNames,
		FrozenSkillBodies:             frozenSkillBodies,
		LocalEnvPolicy:                localEnvPolicyName(s.currentEnv()),
		ResultSchema:                  resultSchema,
		DelegationAllowance:           args.DelegationAllowance,
		WorkingDir:                    s.currentEnv().WorkingDirectory(),
		Isolation:                     isolationName,
		Sandbox:                       sandboxSnapshot,
		Config:                        childConfig,
		SharedTaskStoreOwnerSessionID: sharedTaskStoreOwnerSessionID,
		ParentWatchGranted:            args.WatchParent,
		Provenance:                    s.activeCausalProvenance(),
		Resumable:                     true,
	}
	if selection.agent != nil {
		descriptor.TaskTemplates = append(descriptor.TaskTemplates, selection.agent.Tasks...)
	}
	if callID, ok := ctx.Value(ctxToolCallID).(string); ok {
		descriptor.OriginToolCallID = callID
	}
	if itemID, ok := ctx.Value(ctxToolItemID).(string); ok {
		descriptor.OriginItemID = itemID
	}
	var project identifier.Project
	if isolationName == "worktree" {
		local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
		if !ok {
			return delegatestore.Descriptor{}, identifier.Project{}, errors.New(`delegate isolation:"worktree" requires a local execution environment`)
		}
		project, err = resolveWorktreeProject(local, local.WorkingDirectory())
		if err != nil {
			return delegatestore.Descriptor{}, identifier.Project{}, fmt.Errorf("delegate isolation: resolve project: %w", err)
		}
		root, err := s.worktreeRootForProject(s.currentStateDir(), project)
		if err != nil {
			return delegatestore.Descriptor{}, identifier.Project{}, err
		}
		descriptor.WorkingDir = filepath.Join(root, project.ID)
	}
	return descriptor, project, nil
}

func stableDelegateRole(selection subagentModelSelection, childCanDelegate bool, s *Session) (string, string) {
	if selection.agent != nil && strings.TrimSpace(selection.agent.SystemPrompt) != "" {
		return selection.agent.Name, selection.agent.SystemPrompt
	}
	if selection.agent == nil && childCanDelegate {
		return "subagent", defaultDelegatingSubagentInstructions
	}
	if subagentAgent, ok := s.pluginAgents["subagent"]; ok {
		return "subagent", subagentAgent.SystemPrompt
	}
	return "subagent", defaultSubagentInstructions
}

func stableDelegateSandboxSnapshot(policy *sandbox.SandboxPolicy) *delegatestore.SandboxSnapshot {
	if policy == nil || policy.Mode == sandbox.ModeOff {
		return nil
	}
	result := &delegatestore.SandboxSnapshot{
		Mode:               policy.Mode.String(),
		DenylistAdd:        append([]string(nil), policy.DenylistAdd...),
		DenylistRemove:     append([]string(nil), policy.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), policy.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), policy.ExtraReadRoots...),
	}
	if policy.Network != nil {
		network := *policy.Network
		result.Network = &network
	}
	return result
}

func (runtime delegateRuntime) prepareIsolation(ctx context.Context, reservation *delegateStartReservation, project identifier.Project, requestedSandbox *sandbox.SandboxPolicy) (delegateIsolation, error) {
	s := runtime.owner
	isolation := delegateIsolation{worktreeProject: project}
	workingDir := reservation.worktreePath
	if workingDir != "" {
		path, _, _, _, createdProject, err := s.createDelegateWorktree(ctx, reservation.delegateID)
		if err != nil {
			return isolation, err
		}
		isolation.worktreePath = path
		isolation.worktreeProject = createdProject
		if filepath.Clean(path) != filepath.Clean(workingDir) {
			isolation.cleanup(s, reservation.delegateID)
			return delegateIsolation{}, fmt.Errorf("delegate isolation path %q does not match reserved path %q", path, workingDir)
		}
	}
	env, ownsFresh, err := s.prepareSubagentEnvironment(workingDir, requestedSandbox)
	if err != nil {
		isolation.cleanup(s, reservation.delegateID)
		return delegateIsolation{}, err
	}
	isolation.env = env
	isolation.ownsFreshEnv = ownsFresh
	return isolation, nil
}

func (isolation delegateIsolation) cleanup(s *Session, delegateID string) {
	if isolation.ownsFreshEnv {
		if local, ok := isolation.env.(*execenv.LocalExecutionEnvironment); ok {
			local.DisposeSandboxScratch()
		}
	}
	if isolation.worktreePath != "" {
		s.rollbackFreshDelegateWorktree(delegateID, isolation.worktreePath, isolation.worktreeProject)
	}
}

func (runtime delegateRuntime) construct(_ context.Context, args delegateArgs, selection subagentModelSelection, started delegateStartCommit, isolation delegateIsolation) (*preparedSubagentRun, error) {
	s := runtime.owner
	ctx := started.ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if started.descriptor.OriginToolCallID != "" {
		ctx = context.WithValue(ctx, ctxToolCallID, started.descriptor.OriginToolCallID)
	}
	if started.descriptor.OriginItemID != "" {
		ctx = context.WithValue(ctx, ctxToolItemID, started.descriptor.OriginItemID)
	}
	ctx = context.WithValue(ctx, ctxParentDelegateID, started.lease.delegateID)
	ctx = context.WithValue(ctx, ctxDelegationAllowance, started.descriptor.DelegationAllowance)
	ctx = context.WithValue(ctx, delegateChildSessionIDContextKey{}, started.descriptor.ChildSessionID)
	ctx = context.WithValue(ctx, delegatePreparedEnvironmentContextKey{}, delegatePreparedEnvironment{
		env:              isolation.env,
		ownsFresh:        isolation.ownsFreshEnv,
		stableController: true,
	})
	if requested := started.descriptor.Sandbox; requested != nil {
		ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, sandboxPolicyFromStableSnapshot(requested))
	}
	if started.descriptor.Isolation != "" {
		ctx = context.WithValue(ctx, ctxIsolation, started.descriptor.Isolation)
	}
	if started.descriptor.ParentWatchGranted {
		ctx = context.WithValue(ctx, ctxWatchParent, true)
	}
	prepared, err := s.prepareStableDelegateRun(ctx, started.descriptor, started.descriptor.ParentWatchGranted, selection)
	if err != nil {
		return nil, err
	}
	if err := started.ctx.Err(); err != nil {
		prepared.disposeUnadopted()
		return nil, err
	}
	if prepared.sub.id != started.descriptor.ChildSessionID {
		prepared.disposeUnadopted()
		return nil, fmt.Errorf("constructed child session %q does not match durable descriptor %q", prepared.sub.id, started.descriptor.ChildSessionID)
	}
	prepared.runCancel()
	runContext, runCancel := context.WithCancel(started.ctx)
	runContext = context.WithValue(runContext, delegateRunLeaseContextKey{}, started.lease)
	runContext = context.WithValue(runContext, delegatePreseededInputContextKey{}, delegatePreseededInput{sessionID: prepared.sub.id, input: prepared.input})
	prepared.runCtx = runContext
	prepared.runCancel = runCancel
	prepared.sub.mu.Lock()
	prepared.sub.cancel = runCancel
	prepared.sub.startedAt = started.startedAt
	prepared.sub.mu.Unlock()
	return prepared, nil
}

func (runtime delegateRuntime) restoreIdle(started delegateStartCommit) (*subagent, bool, error) {
	s := runtime.owner
	if s == nil || started.lease.delegateID == "" {
		return nil, false, errors.New("delegate restore reservation is unavailable")
	}
	descriptor := cloneDelegateStartDescriptor(started.descriptor)
	if retained := s.subagents.get(descriptor.ChildSessionID); retained != nil && retained.sess != nil {
		return retained, false, nil
	}
	if err := s.reclaimDelegateRuntimeCapacity(1); err != nil {
		return nil, false, err
	}
	meta, err := schema.LoadSessionMeta(s.stateDir, descriptor.ChildSessionID)
	if err != nil {
		return nil, false, fmt.Errorf("load committed delegate session metadata: %w", err)
	}
	if meta.ID != descriptor.ChildSessionID {
		return nil, false, errors.New("committed delegate session metadata has the wrong identity")
	}
	meta.Config = descriptor.Config.Clone()
	profile, err := s.resolveDelegateRestoreProfileRef(s.currentProfile(), descriptor.ResolvedProfileID, descriptor.ResolvedModel)
	if err != nil {
		return nil, false, fmt.Errorf("resolve committed delegate profile: %w", err)
	}
	resultSchema := make(map[string]any)
	if len(descriptor.ResultSchema) != 0 {
		if err := json.Unmarshal(descriptor.ResultSchema, &resultSchema); err != nil {
			return nil, false, fmt.Errorf("decode committed delegate result schema: %w", err)
		}
		if len(resultSchema) != 0 {
			profile = provider.WithCommunicateOutputSchema(profile, resultSchema)
		}
	}
	policy := sandboxPolicyFromStableSnapshot(descriptor.Sandbox)
	childEnv, ownsFresh, err := s.prepareSubagentEnvironment(descriptor.WorkingDir, policy)
	if err != nil {
		return nil, false, err
	}
	discardEnv := true
	defer func() {
		if discardEnv && ownsFresh {
			if local, ok := childEnv.(*execenv.LocalExecutionEnvironment); ok {
				local.DisposeSandboxScratch()
			}
		}
	}()
	if childEnv == nil || childEnv.WorkingDirectory() != descriptor.WorkingDir || localEnvPolicyName(childEnv) != descriptor.LocalEnvPolicy || !frozenStableDelegateSandboxMatches(childEnv, descriptor.Sandbox) {
		return nil, false, errors.New("committed delegate environment is unavailable")
	}
	activatedSkillBodies, err := restoreFrozenSkillBodies(descriptor.FrozenSkillNames, descriptor.FrozenSkillBodies)
	if err != nil {
		return nil, false, err
	}
	var sharedStore *task.TaskStore
	if descriptor.Config.ShareTasksWithChildren {
		sharedStore, err = s.resolveStableSharedTaskStore(descriptor.SharedTaskStoreOwnerSessionID)
		if err != nil {
			return nil, false, err
		}
	}
	restoreCfg := RestoreSessionConfig{
		StateDir:                s.stateDir,
		Project:                 s.cfg.Project,
		ResolveProfile:          s.resolveProfile,
		AcquireSessionOwnership: s.cfg.AcquireSessionOwnership,
		ModelFallbacks:          append([]string(nil), descriptor.Config.ModelFallbacks...),
		LLMRetryPolicy:          s.cfg.LLMRetryPolicy,
		LLMSleep:                s.cfg.LLMSleep,
		clock:                   s.clock,
		testOnly:                s.cfg.testOnly,
		ForceRealIO:             s.cfg.ForceRealIO,
		artifactStore:           s.artifactStore,
		deferRestoreSideEffects: true,
		spawn: spawnConfig{
			delegateController:            s.delegateController,
			delegateRootSessionID:         s.delegateRootSessionID,
			owningDelegateID:              started.lease.delegateID,
			subscriberCount:               s.subscriberCountFn,
			parentSessionID:               s.id,
			parentToolCallID:              descriptor.OriginToolCallID,
			parentItemID:                  descriptor.OriginItemID,
			parentDelegateID:              started.lease.delegateID,
			forwardJobEvent:               s.jobManager.forwardEvent,
			descendantEvent:               s.cfg.spawn.descendantEvent,
			parentSteer:                   s.SteerWithProvenance,
			parentSystemNotification:      s.routeSystemNotification,
			subagentTask:                  descriptor.Task,
			depth:                         s.depth + 1,
			delegationAllowance:           descriptor.DelegationAllowance,
			sharedTaskStore:               sharedStore,
			sharedTaskStoreOwnerSessionID: descriptor.SharedTaskStoreOwnerSessionID,
			rolePromptOverride:            descriptor.FrozenRolePrompt,
			activatedSkillBodies:          activatedSkillBodies,
			toolNameCeiling:               append([]string(nil), descriptor.ToolNameCeiling...),
			isolation:                     descriptor.Isolation,
			communicateOutputSchema:       cloneMap(resultSchema),
			parentWatchGranted:            descriptor.ParentWatchGranted,
			jobActivityClock:              s.jobActivityClock,
		},
	}
	child, err := RestoreSessionFromMetaWithConfig(s.client, profile, childEnv, meta, restoreCfg)
	if err != nil {
		return nil, false, err
	}
	discardEnv = false
	if child.delegateController != s.delegateController || child.owningDelegateID != started.lease.delegateID {
		child.discardRestoredCandidate()
		return nil, false, errors.New("restored delegate did not inherit the exact controller binding")
	}
	for name := range child.reg.RegisteredNames() {
		if !hasString(descriptor.ToolNameCeiling, name) {
			child.discardRestoredCandidate()
			return nil, false, fmt.Errorf("restored delegate tool %q exceeds the committed ceiling", name)
		}
	}
	if len(descriptor.TaskTemplates) != 0 && len(child.getOrCreateTaskStore().View()) == 0 {
		if err := child.getOrCreateTaskStore().PopulateFromTemplates(descriptor.TaskTemplates, nil); err != nil {
			child.discardRestoredCandidate()
			return nil, false, fmt.Errorf("restore committed delegate tasks: %w", err)
		}
	}
	now := s.sclock().Now()
	stableDescriptor := cloneDelegateStartDescriptor(descriptor)
	sub := &subagent{
		id:               descriptor.ChildSessionID,
		sess:             child,
		emit:             s.emit,
		status:           SubagentCompleted,
		nudgeEnabled:     descriptor.Config.AgentName == "subagent",
		agentType:        descriptor.AgentType,
		createdAt:        now,
		startedAt:        now,
		endedAt:          &now,
		stableDescriptor: &stableDescriptor,
		ownsEnv:          ownsFresh,
	}
	child.SetNotifyFunc(func() { s.driveChildIfNotStopGated(sub) })
	return sub, true, nil
}

func (runtime delegateRuntime) restoreIdleForSend(started delegateStartCommit) (*subagent, bool, func(*subagent, error), error) {
	s := runtime.owner
	finish := func(*subagent, error) {}
	if s == nil {
		return nil, false, finish, errors.New("delegate restore reservation is unavailable")
	}
	childID := strings.TrimSpace(started.descriptor.ChildSessionID)
	if childID == "" {
		return nil, false, finish, errors.New("delegate restore child identity is unavailable")
	}
	existing, pending, leader, err := s.subagents.beginReconstruction(childID)
	if err != nil {
		return nil, false, finish, err
	}
	if !leader {
		if existing != nil {
			return existing, false, finish, nil
		}
		reconstructed, waitErr := pending.wait()
		return reconstructed, false, finish, waitErr
	}
	finish = func(sub *subagent, restoreErr error) {
		s.subagents.finishReconstruction(childID, pending, sub, restoreErr)
	}
	sub, restored, restoreErr := runtime.restoreIdle(started)
	return sub, restored, finish, restoreErr
}

func sandboxPolicyFromStableSnapshot(snapshot *delegatestore.SandboxSnapshot) *sandbox.SandboxPolicy {
	if snapshot == nil {
		return nil
	}
	mode, err := sandbox.ParseMode(snapshot.Mode)
	if err != nil {
		return nil
	}
	policy := &sandbox.SandboxPolicy{
		Mode:               mode,
		DenylistAdd:        append([]string(nil), snapshot.DenylistAdd...),
		DenylistRemove:     append([]string(nil), snapshot.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), snapshot.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), snapshot.ExtraReadRoots...),
	}
	if snapshot.Network != nil {
		network := *snapshot.Network
		policy.Network = &network
	}
	return policy
}

func (s *Session) resolveStableSharedTaskStore(ownerSessionID string) (*task.TaskStore, error) {
	if s == nil || s.delegateController == nil || strings.TrimSpace(ownerSessionID) == "" {
		return nil, errors.New("committed shared task store owner is unavailable")
	}
	c := s.delegateController
	c.mu.Lock()
	var owner *Session
	if c.rootRuntime != nil && c.rootRuntime.id == ownerSessionID {
		owner = c.rootRuntime
	}
	if owner == nil {
		for _, live := range c.live {
			if live != nil && live.runtime != nil && live.runtime.id == ownerSessionID {
				owner = live.runtime
				break
			}
		}
	}
	c.mu.Unlock()
	if owner == nil {
		return nil, fmt.Errorf("committed shared task store owner %q is not resident", ownerSessionID)
	}
	return owner.getOrCreateTaskStore(), nil
}

func (runtime delegateRuntime) adopt(prepared *preparedSubagentRun) error {
	s := runtime.owner
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("session is closed")
	}
	s.subagents.track(prepared.sub)
	s.sendersWG.Add(1)
	s.mu.Unlock()
	return nil
}

func (runtime delegateRuntime) preseedInput(child *Session, input, transcriptPath string) error {
	child.maybeAppendEnvironmentContext()
	message := buildUserInputMessage(input, nil)
	if observer := child.cfg.testOnly.delegateInitialInputAppend; observer != nil {
		observer(child)
	}
	if err := child.appendTurnWithDurableTranscriptMessage(schema.TurnUserInput, message, message); err != nil {
		return err
	}
	data, err := readStrictChildTranscript(transcriptPath, child.ID(), child.strictTranscriptMaxLineBytes)
	if err != nil {
		return fmt.Errorf("read back child input transcript: %w", err)
	}
	for _, entry := range slices.Backward(data.Entries) {
		turn := entry.Turn
		if turn.Kind == schema.TurnUserInput {
			if turn.Message.Text() != input {
				return errors.New("read back child input transcript: latest user input differs")
			}
			return nil
		}
	}
	return errors.New("read back child input transcript: user input is absent")
}

func (runtime delegateRuntime) failCommittedStart(started delegateStartCommit, isolation delegateIsolation, prepared *preparedSubagentRun, controllerAttached bool, constructionErr error, reason string) delegateResult {
	finish := delegatePermanentStartFailure(constructionErr, reason)
	var runtimeForClose *Session
	if controllerAttached && prepared != nil {
		runtimeForClose = prepared.sub.sess
	}
	plans, claimedForClose, finishErr := runtime.owner.delegateController.FailCommittedStart(started.lease, finish, reason, runtimeForClose)
	runtime.owner.delegateController.emitDelegateUpdates(plans)
	if committedStartFailureDisposition(finishErr) == delegateCommittedStartFailureStopWon {
		if prepared != nil && (!controllerAttached || claimedForClose) {
			prepared.runCancel()
			prepared.disposeUnadopted()
		}
		return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, constructionErr)
	}
	if finishErr != nil {
		retainErr := runtime.retainFailedStartCandidate(prepared)
		return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(constructionErr, finishErr, retainErr))
	}
	if prepared != nil {
		prepared.runCancel()
		prepared.disposeUnadopted()
	}
	isolation.cleanup(runtime.owner, started.lease.delegateID)
	return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, constructionErr)
}

func (runtime delegateRuntime) retainFailedStartCandidate(prepared *preparedSubagentRun) error {
	if prepared == nil {
		return nil
	}
	prepared.runCancel()
	existing, retained, err := runtime.owner.subagents.trackIfAbsent(prepared.sub)
	if err != nil {
		prepared.disposeUnadopted()
		return err
	}
	if retained || existing == prepared.sub {
		return nil
	}
	prepared.disposeUnadopted()
	return errDelegateTargetBusy
}

func (runtime delegateRuntime) failAdoptedStart(started delegateStartCommit, isolation delegateIsolation, prepared *preparedSubagentRun, startErr error, reason string) delegateResult {
	finish := delegatePermanentStartFailure(startErr, reason)
	plans, _, finishErr := runtime.owner.delegateController.FailCommittedStart(started.lease, finish, reason, nil)
	runtime.owner.delegateController.emitDelegateUpdates(plans)
	if finishErr != nil {
		runtime.retainAdoptedWithoutLaunch(prepared)
		return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, errors.Join(startErr, finishErr))
	}
	runtime.discardAdopted(prepared)
	isolation.cleanup(runtime.owner, started.lease.delegateID)
	return stableDelegateResult(started.descriptor, started.lease.delegateID, started.plan, plans, startErr)
}

func (runtime delegateRuntime) retainAdoptedWithoutLaunch(prepared *preparedSubagentRun) {
	prepared.runCancel()
	runtime.owner.sendersWG.Done()
}

func (runtime delegateRuntime) discardAdopted(prepared *preparedSubagentRun) {
	runtime.owner.subagents.remove(prepared.sub.id)
	prepared.runCancel()
	prepared.disposeUnadopted()
	runtime.owner.sendersWG.Done()
}

func delegatePermanentStartFailure(err error, reason string) delegateFinish {
	message := "delegate start failed"
	if err != nil {
		message = err.Error()
	}
	if len(message) > 512 {
		message = message[:512]
	}
	raw, _ := json.Marshal(message)
	packet := &delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: raw}
	return delegateFinish{
		outcome:     delegatestore.OutcomeFailed,
		disposition: delegatestore.DispositionTerminalError,
		reason:      reason,
		packet:      packet,
	}
}

func stableDelegateResult(descriptor delegatestore.Descriptor, delegateID string, committed delegateUpdatePlan, plans delegateMutationPlans, err error) delegateResult {
	snapshot := latestDelegateMutationSnapshot(delegateID, committed, plans)
	resumable := snapshot.resumable
	status := jobstore.Status(snapshot.lifecycle)
	if status == "" {
		status = jobstore.StatusRunning
	}
	result := delegateResult{
		DelegateID:          delegateID,
		ChildSessionID:      descriptor.ChildSessionID,
		Type:                delegateResourceType,
		Status:              status,
		Resumable:           &resumable,
		RunningInBackground: true,
		TranscriptRef:       descriptor.TranscriptRef,
		Model:               descriptor.ResolvedProfileID + "/" + descriptor.ResolvedModel,
		Err:                 err,
	}
	if snapshot.lastOutcome != nil {
		result.Reason = snapshot.lastOutcome.Reason
	}
	if descriptor.Sandbox != nil {
		network := true
		if descriptor.Sandbox.Network != nil {
			network = *descriptor.Sandbox.Network
		}
		result.Sandbox = &delegateSandboxReport{Mode: descriptor.Sandbox.Mode, Network: network}
	}
	return result
}

func latestDelegateMutationSnapshot(delegateID string, committed delegateUpdatePlan, plans delegateMutationPlans) delegateSnapshot {
	var latest delegateSnapshot
	for _, row := range committed.rows {
		if row.id == delegateID {
			latest = row
		}
	}
	if latest.id == "" && len(committed.rows) > 0 {
		latest = committed.rows[len(committed.rows)-1]
	}
	for _, update := range plans.updates {
		for _, row := range update.rows {
			if row.id == delegateID {
				latest = row
			}
		}
	}
	return latest
}

func (s *Session) bootstrapDelegateResources() error {
	if inherited := s.cfg.spawn.delegateController; inherited != nil {
		s.delegateController = inherited
		s.delegateRootSessionID = s.cfg.spawn.delegateRootSessionID
		s.owningDelegateID = s.cfg.spawn.owningDelegateID
		return nil
	}
	if err := rejectLegacyDelegateState(s.stateDir, s.id); err != nil {
		return err
	}
	path := filepath.Join(jobsDir(s.stateDir, s.id), "delegates.jsonl")
	store, err := delegatestore.Open(path)
	if err != nil {
		return fmt.Errorf("open delegate store: %w", err)
	}
	controller, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:               store,
		rootRuntime:         s,
		rootSessionID:       s.id,
		stateDir:            s.stateDir,
		worktreeRoot:        filepath.Join(jobsDir(s.stateDir, s.id), "worktrees"),
		turnLimit:           s.cfg.MaxConcurrentDelegateTurns,
		driveLimit:          defaultMaxConcurrentDriveTurns,
		maxRetainedTerminal: s.cfg.MaxRetainedTerminal,
		now:                 s.sclock().Now,
		attentionOpen:       s.cfg.testOnly.delegateAttentionOpenWriter,
	})
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("open delegate controller: %w", err)
	}
	if err := repairStableWatchDeliveriesForBootstrap(controller); err != nil {
		_ = store.Close()
		return fmt.Errorf("repair stable watch deliveries: %w", err)
	}
	reconcileDeliveries, err := reconcileDelegateResourcesForBootstrap(controller)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("reconcile delegate resources: %w", err)
	}
	if err := repairStableShellAttentionForBootstrap(controller); err != nil {
		_ = store.Close()
		return fmt.Errorf("repair stable shell attention: %w", err)
	}
	missingInputs, err := missingDelegateRestoreInputs(s.stateDir, controller, s.delegateRestoreStat, s.delegateRestoreReadFile)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("inspect delegate restore inputs: %w", err)
	}
	missingPlans, err := controller.closeMissingRestoreInputs(missingInputs)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("close delegates with missing restore inputs: %w", err)
	}
	if err := repairPermanentlyUnreachableDelegateAttention(controller); err != nil {
		_ = store.Close()
		return fmt.Errorf("repair unreachable delegate attention: %w", err)
	}
	if err := controller.rearmDelegateAttentionFromTranscripts(); err != nil {
		_ = store.Close()
		return fmt.Errorf("rearm delegate attention: %w", err)
	}
	deliveryPlans := append([]delegateDeliveryPlan(nil), reconcileDeliveries...)
	deliveryPlans = append(deliveryPlans, missingPlans.deliveries...)
	deliveryPlans = append(deliveryPlans, controller.ReplayDeliveries()...)
	pendingDeliveries, err := prepareColdDelegateDeliveryReplay(controller, deliveryPlans)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("replay committed delegate deliveries: %w", err)
	}
	s.delegateController = controller
	s.delegateRootSessionID = s.id
	s.ownsDelegateController = true
	controller.emitUpdate = s.emitStableDelegateUpdate
	s.pendingDelegateDeliveries = append(s.pendingDelegateDeliveries, pendingDeliveries...)
	return nil
}

func (s *Session) emitStableDelegateUpdate(plan delegateUpdatePlan) {
	if s == nil || s.delegateController == nil {
		return
	}
	now := s.sclock().Now().UTC()
	rootID := s.delegateController.rootSessionID
	for _, row := range plan.rows {
		ownerID := row.descriptor.OwnerSessionID
		data := delegateUpdatedDataFromStatus(delegateStatusInfoFromSnapshot(now, rootID, row))
		if runtime := s.delegateController.runtimeForDelegateOwner(row); runtime != nil {
			runtime.emitWithProvenance(events.EventDelegateUpdated, data, row.descriptor.Provenance)
			continue
		}
		s.mu.Lock()
		forward := s.cfg.spawn.descendantEvent
		s.mu.Unlock()
		if ownerID == "" || forward == nil {
			continue
		}
		event := events.New(data)
		event.Timestamp = now
		event.SessionID = ownerID
		event.Provenance = provenance.Clone(row.descriptor.Provenance)
		forward(event)
	}
}

func (c *delegateTreeController) runtimeForDelegateOwner(row delegateSnapshot) *Session {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if row.descriptor.OwnerSessionID == c.rootSessionID {
		return c.rootRuntime
	}
	if live := c.live[row.parentID]; live != nil && live.runtime != nil && live.runtime.ID() == row.descriptor.OwnerSessionID {
		return live.runtime
	}
	return nil
}

func reconcileDelegateResourcesForBootstrap(controller *delegateTreeController) ([]delegateDeliveryPlan, error) {
	var deliveries []delegateDeliveryPlan
	for {
		evidence, err := collectDelegateReconcileEvidence(controller.stateDir, controller.ReconcileRequirements())
		if err != nil {
			return nil, fmt.Errorf("collect evidence: %w", err)
		}
		plans, err := controller.Reconcile(evidence)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, plans.deliveries...)
		for _, plan := range plans.shellRepairs {
			if err := executeDelegateShellRepair(plan, controller.now()); err != nil {
				return nil, err
			}
		}
		for _, plan := range plans.attention {
			if err := controller.executeDelegateAttentionCleanup(plan); err != nil {
				return nil, err
			}
		}

		controller.mu.Lock()
		stopPending := controller.stop != nil
		controller.mu.Unlock()
		if !stopPending {
			return deliveries, nil
		}
		if len(plans.updates) == 0 && len(plans.deliveries) == 0 && len(plans.shellRepairs) == 0 && len(plans.attention) == 0 {
			return nil, errors.New("restored delegate stop made no reconciliation progress")
		}
	}
}

func missingDelegateRestoreInputs(
	stateDir string,
	controller *delegateTreeController,
	stat func(string) (os.FileInfo, error),
	readFile func(string) ([]byte, error),
) (map[string]string, error) {
	controller.mu.Lock()
	ids := make([]string, 0, len(controller.durable))
	descriptors := make(map[string]delegatestore.Descriptor, len(controller.durable))
	for id, aggregate := range controller.durable {
		if aggregate == nil || !aggregate.Resumable || aggregate.Phase != delegatestore.PhaseIdle {
			continue
		}
		ids = append(ids, id)
		descriptors[id] = cloneDelegateStartDescriptor(aggregate.Descriptor)
	}
	controller.mu.Unlock()
	sort.Strings(ids)
	reasons := make(map[string]string)
	for _, id := range ids {
		reason, err := missingDelegateRestoreInputReason(stateDir, descriptors[id], stat, readFile)
		if err != nil {
			return nil, fmt.Errorf("delegate %s: %w", id, err)
		}
		if reason != "" {
			reasons[id] = reason
		}
	}
	return reasons, nil
}

func missingDelegateRestoreInputReason(
	stateDir string,
	descriptor delegatestore.Descriptor,
	stat func(string) (os.FileInfo, error),
	readFile func(string) ([]byte, error),
) (string, error) {
	childID := strings.TrimSpace(descriptor.ChildSessionID)
	if childID == "" || strings.TrimSpace(descriptor.Task) == "" || strings.TrimSpace(descriptor.AgentType) == "" || strings.TrimSpace(descriptor.ResolvedProfileID) == "" || strings.TrimSpace(descriptor.ResolvedModel) == "" {
		return notResumableMissingDelegateResumeMetadata, nil
	}
	_, transcriptChildID, err := decodeRef(descriptor.TranscriptRef)
	if err != nil || transcriptChildID != childID {
		return notResumableParentLinkageUnavailable, nil //nolint:nilerr // malformed durable linkage is a classified non-resumable state, not an operational failure
	}
	if strings.TrimSpace(stateDir) == "" {
		return notResumableMissingChildSessionMeta, nil
	}
	if workingDir := strings.TrimSpace(descriptor.WorkingDir); workingDir != "" {
		if _, err := stat(workingDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return notResumableWorkingDirMissing, nil
			}
			return "", fmt.Errorf("stat working directory %s: %w", workingDir, err)
		}
	}
	metaPath := filepath.Join(stateDir, sessionsSubdir, childID+".meta.json")
	metaBytes, err := readFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notResumableMissingChildSessionMeta, nil
		}
		return "", fmt.Errorf("read child session metadata %s: %w", childID, err)
	}
	var meta schema.SessionMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return notResumableCorruptChildSessionMeta, nil //nolint:nilerr // malformed durable metadata is a classified non-resumable state, not an operational failure
	}
	if strings.TrimSpace(meta.ID) != childID {
		return notResumableCorruptChildSessionMeta, nil
	}
	path := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	if _, err := stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notResumableMissingChildTranscript, nil
		}
		return "", fmt.Errorf("stat child transcript %s: %w", childID, err)
	}
	if _, err := validateStrictChildTranscript(path, childID, 0); err != nil {
		if delegateRestoreOperationalIOError(err) {
			return "", fmt.Errorf("validate child transcript %s: %w", childID, err)
		}
		if errors.Is(err, errStrictChildTranscriptSessionMismatch) {
			return notResumableTranscriptSessionMismatch, nil
		}
		if errors.Is(err, errStrictChildTranscriptCorrupt) || errors.Is(err, transcript.ErrUnsupportedFormat) {
			return notResumableCorruptChildTranscript, nil
		}
		return "", fmt.Errorf("validate child transcript %s: %w", childID, err)
	}
	return "", nil
}

func (s *Session) delegateRestoreStat(path string) (os.FileInfo, error) {
	if stat := s.cfg.testOnly.delegateRestoreStat; stat != nil {
		return stat(path)
	}
	return os.Stat(path)
}

func (s *Session) delegateRestoreReadFile(path string) ([]byte, error) {
	if readFile := s.cfg.testOnly.delegateRestoreReadFile; readFile != nil {
		return readFile(path)
	}
	return os.ReadFile(path)
}

func delegateRestoreOperationalIOError(err error) bool {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return false
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return true
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno)
}

func (s *Session) closeOwnedDelegateStore() error {
	return s.closeOwnedDelegateStoreWithContext(context.Background())
}

func (s *Session) closeOwnedDelegateStoreWithContext(ctx context.Context) error {
	if s == nil || !s.ownsDelegateController || s.delegateController == nil || s.delegateController.store == nil {
		return nil
	}
	return s.delegateController.closeStoreAfterStopReconcileDriver(ctx)
}

func (s *Session) closeOwnedDelegateRuntimeTree(ctx context.Context) error {
	if s == nil || !s.ownsDelegateController || s.delegateController == nil {
		return nil
	}
	return s.delegateController.closeRuntimeTree(ctx, func(child *Session) {
		child.close(ctx, false)
	})
}
