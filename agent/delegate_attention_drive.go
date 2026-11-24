package agent

import (
	"context"
	"errors"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func bindDelegateActivity(child *Session, controller *delegateTreeController, lease delegateLease) {
	if child == nil || controller == nil {
		return
	}
	owner := controller.delegateOwnerRuntime(lease)
	bindDelegateActivityToOwner(child, controller, lease, owner)
}

func bindDelegateActivityToOwner(child *Session, controller *delegateTreeController, lease delegateLease, owner *Session) {
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
	child.cfg.spawn.parentJobActivity = func(_ string, phase string) {
		_ = controller.ReportActivityPhase(lease, child.sclock().Now(), phase)
	}
	// The intent closure binds to this child's own lease, the fence that keeps
	// a descendant's tool calls off every other row: each child reports to its
	// own delegate id, never its parent's.
	child.cfg.spawn.parentJobIntent = func(_ string, intent string) {
		_ = controller.ReportIntent(lease, child.sclock().Now(), intent)
	}
	jm := child.jobManager
	child.mu.Unlock()
	if jm != nil {
		jm.bindDelegateParent(controller, lease, forward, child)
	}
}

func (s *Session) driveDelegateAttention(sub *subagent) bool {
	if s == nil || sub == nil || sub.sess == nil || sub.sess.owningDelegateID == "" || s.delegateController == nil || sub.sess.delegateController != s.delegateController {
		return false
	}
	// A row a settled request already presented whose resolution append failed
	// must leave the queue before any drive may select this delegate again: the
	// model read it, so the only work left is the retried resolution.
	if err := sub.sess.flushAwaitingDelegateAttentionResolutions(); err != nil {
		sub.sess.warnDelegateAttentionFailed(delegateAttentionResolveLabel, sub.sess.owningDelegateID, err)
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	ids, err := sub.sess.automaticDelegateAttentionIDs()
	if err != nil {
		s.emit(warningDataFromError("inspect delegate attention", err))
		return true
	}
	if len(ids) == 0 {
		// The durable queue holds no unheld row, so this delegate owes no
		// attention a drive could present. Let the notification drive proceed.
		return false
	}
	if observer := s.cfg.testOnly.delegateAttentionBeforeDriveClaim; observer != nil {
		observer(sub)
	}
	// Claim the child for the WHOLE start, not just for this check. Everything
	// between here and launchAcceptedDelegateAttention is durable work
	// (ReserveAttention, CommitStart, the delegate update emit), and the run
	// goroutine only sets
	// running at the far end of it. Without the claim a wake-edge drive landing
	// in that gap reads an idle child: the reservation is already consumed and
	// the attention is no longer pending, so driveDelegateAttention itself
	// declines, and driveChildIfNotStopGated falls through to
	// driveSubagentNotificationTurn, which starts a second, UNLEASED turn on the
	// session this generation is about to run. The two turns then share one
	// drain ladder and the unleased one can pop the run's follow-up, leaving the
	// generation to settle attention-only instead of report-required. The claim
	// is the drive flag every other guard already reads, and it is handed over
	// under the same sub.mu hold that sets running.
	sub.mu.Lock()
	// childCommittedSendStart is consulted here, at the decision point, so every
	// path into the attention drive is gated at the depth the decision is made
	// rather than relying on each caller to remember (#940).
	// drivePendingDelegateAttention reaches this primitive directly with no
	// caller-side gate.
	// startBlockedLocked is the shared drive-guard predicate delegate_send reads
	// through takeSendDriveGuard, so the two busy lists cannot drift. The
	// attention-only extras are layered on top.
	blocked := sub.startBlockedLocked() || sub.closed || sub.fatalRunGated || s.childCommittedSendStart(sub.sess.id)
	// Only the strand-shaped holds name a predicate in the refusal warning
	// (#6330): a busy run, a concurrent drive claim or a disposal gate is
	// presenting or about to present the attention itself, and a closed child
	// or a committed send leaves the resolution to its teardown or reply, so
	// warning on those healthy shapes would bury the strand signal in noise.
	// The holds that strand a lane are a finalizing runtime — its drain takes
	// no turn for a report — and the fatal-run gate.
	refusal := ""
	switch {
	case sub.fatalRunGated:
		refusal = delegateAttentionRefusedFatalRunGated
	case sub.finalizing:
		refusal = delegateAttentionRefusedStartBlocked
	}
	if blocked && sub.driving {
		sub.attentionDriveRefused = true
	}
	if !blocked {
		sub.driving = true
	}
	sub.mu.Unlock()
	if blocked {
		if refusal != "" {
			s.warnDelegateAttentionRefused(sub.sess.owningDelegateID, refusal)
		}
		return true
	}
	launched := false
	defer func() {
		if launched {
			return
		}
		if observer := s.cfg.testOnly.delegateAttentionBeforeGuardRelease; observer != nil {
			observer(sub)
		}
		// The re-drive re-drives again only if a drive is refused on its own
		// guard, so one that keeps losing stops (#3723).
		if releaseDriveGuard(sub) {
			s.redriveLiveChild(sub.sess.id)
		}
	}()
	s.mu.Lock()
	closed := s.closingOrClosedLocked()
	s.mu.Unlock()
	if closed {
		return true
	}
	// The first read precedes the child claim. Another drive may present and
	// finish that attention generation before this drive takes the claim, so
	// select again while the claim fences every competing drive. The durable
	// queue is the authority: a row another generation consumed is simply
	// gone.
	pending, err := sub.sess.automaticDelegateAttentionIDs()
	if err != nil {
		s.emit(warningDataFromError("inspect delegate attention", err))
		return true
	}
	if len(pending) == 0 {
		return false
	}
	ids = pending
	reservation, err := s.delegateController.ReserveAttention(sub.sess, ids[0])
	if err != nil {
		if !errors.Is(err, errDelegateTargetBusy) && !errors.Is(err, errTreeAtCapacity) {
			s.emit(warningDataFromError("reserve delegate attention", err))
		}
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	var commitErr error
	// The seam is runtime-mutable through updateSessionTestConfig, so its
	// read shares the setter's session mutex — the convention every
	// mid-run-mutable test seam follows.
	s.mu.Lock()
	fault := s.cfg.testOnly.delegateAttentionCommitStartFault
	s.mu.Unlock()
	if fault != nil {
		// A test seam, nil in production: its error stands in for a commit the
		// controller refused, so the recovery arms below can be pinned without
		// racing a real refusal. The common failure branch below aborts the
		// start, which is a no-op for a reservation the refusal already
		// released.
		commitErr = fault()
	}
	var started delegateStartCommit
	if commitErr == nil {
		started, commitErr = s.delegateController.CommitStart(reservation)
	}
	if commitErr != nil {
		s.emit(warningDataFromError("commit delegate attention", commitErr))
		// The trigger was never consumed, so there is nothing to reopen: the
		// row is still durably queued and the next drive selects it again. A
		// busy commit already released the reservation; an append failure kept
		// it, so abort it here either way.
		_ = s.delegateController.AbortStart(reservation)
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	if observer := s.cfg.testOnly.delegateAttentionStartCommitted; observer != nil {
		observer(sub)
	}
	s.delegateController.emitDelegateUpdate(started.plan)
	// The generation carries ids[0]. Latch it so the fold keeps its turn and
	// the exit rule can name it; the row itself stays durably queued until a
	// settled request presents it.
	sub.sess.holdAcceptedTrigger(ids[0])
	launchErr := s.launchAcceptedDelegateAttention(sub, started)
	launched = launchErr == nil
	if launchErr == nil {
		// The generation owns the attention now, so this delegate's refusal
		// episode ends: a later refusal warns afresh under its own predicate.
		s.delegateAttentionWarningResolved(delegateAttentionDriveLabel, sub.sess.owningDelegateID)
	}
	if launchErr != nil {
		// FinishGeneration, not FailCommittedRestart: an attention
		// generation's binding is born ready, and FailCommittedRestart refuses
		// ready bindings — its cleanup would no-op and leave the delegate
		// running in the controller's live state forever, ineligible for every
		// later start. The launch refused after the commit, so the generation
		// is failed the way any unlaunched start is, quiescence report
		// included, or the delegate stays refused as still finalizing.
		plans, finishErr := s.delegateController.FinishGeneration(started.lease, delegatePermanentStartFailure(launchErr, "launch_failed"))
		if executeErr := s.executeDelegateMutationPlans(plans); finishErr == nil {
			finishErr = executeErr
		}
		s.releaseFinishedGeneration(started.lease, sub.sess)
		s.emit(warningDataFromError("launch delegate attention", errors.Join(launchErr, finishErr)))
		// The run never started, so no request presented the trigger. Drop the
		// latch and leave the row durably queued for the next drive.
		sub.sess.clearAcceptedDelegateTrigger()
		s.delegateController.retryDelegateAttentionLater()
	}
	return true
}

func (s *Session) launchAcceptedDelegateAttention(sub *subagent, started delegateStartCommit) error {
	if s == nil || sub == nil || sub.sess == nil || s.delegateController == nil {
		return errDelegateDeliveryReceiverUnavailable
	}
	if err := s.reviveValidateIsolationLane(started); err != nil {
		// The refused lane strands the attention this drive owed exactly as
		// a refused notification drive strands its pending: without the
		// stamp every later drive retries the revalidation and the drain
		// counts the owed attention as live deliverable work. A later revive
		// that revalidates the lane successfully clears it.
		s.recordDrainAbandonedChildOnLaneRefusal(sub, "its lane failed revalidation before an attention launch")
		return err
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
	// Hand the start claim over to the run under one hold, so the child never
	// reads idle between the committed start and the run that owns it. The
	// clear is unconditional: the owed-attention bootstrap reaches here
	// without having taken the claim, and clearing a flag it never set is
	// harmless because running is already true under this same hold.
	sub.driving = false
	sub.mu.Unlock()
	bindDelegateActivity(sub.sess, s.delegateController, started.lease)
	s.launchSubagentRun(runCtx, sub, runCancel, "", descriptorProvenance(started.descriptor))
	s.startDelegateQuietWatchdog(started.ctx, started.lease)
	return nil
}

func (s *Session) restoreColdDelegateAttentionRuntime(delegateID string, gates ...*owedBootstrapRestore) (*Session, *subagent, error) {
	if s == nil || s.delegateController == nil || delegateID == "" {
		return nil, nil, errors.New("cold delegate attention restore identity is incomplete")
	}
	retirementRelease, retirementErr := s.beginRetirementMutation("delegate")
	if retirementErr != nil {
		return nil, nil, retirementErr
	}
	defer retirementRelease()
	// Hold the restore window from before the commit through installation, so a
	// lane handoff starting in this span refuses instead of tearing the lane down
	// under the runtime being installed.
	if err := s.delegateController.beginLaneRestore(delegateID); err != nil {
		return nil, nil, err
	}
	defer s.delegateController.endLaneRestore(delegateID)
	started, parentID, err := s.delegateController.idleDelegateRestoreCommit(delegateID)
	if err != nil {
		return nil, nil, err
	}
	if observer := s.delegateRestoreAfterCommit; observer != nil {
		observer(delegateID)
	}
	owner, err := s.restoreColdDelegateOwnerRuntime(parentID, gates...)
	if err != nil {
		return nil, nil, err
	}

	sub, err := s.reconstructDelegateAttentionRuntime(owner, started, delegateRuntimeAttachIdle, gates...)
	return owner, sub, err
}

func (s *Session) reconstructDelegateAttentionRuntime(owner *Session, started delegateStartCommit, mode delegateRuntimeAttachMode, gates ...*owedBootstrapRestore) (*subagent, error) {
	runtime := delegateRuntime{owner: owner}
	sub, restored, finishRestore, err := runtime.restoreIdleForSend(started)
	if err != nil {
		finishRestore(nil, err)
		return nil, err
	}
	if sub == nil || sub.sess == nil {
		err = errors.New("delegate attention runtime is unavailable")
		finishRestore(nil, err)
		return nil, err
	}
	var installation *delegateIdleRuntimeInstallation
	if mode == delegateRuntimeAttachIdle {
		installation, err = s.delegateController.beginIdleRuntimeInstallation(started.lease.delegateID)
		if err != nil {
			if restored {
				sub.sess.discardRestoredCandidate()
			}
			finishRestore(nil, err)
			return nil, err
		}
		defer installation.release()
	}
	attach := func(selected *subagent) error {
		switch mode {
		case delegateRuntimeAttachIdle:
			return installation.attach(selected.sess)
		case delegateRuntimeAttachStarted:
			return s.delegateController.AttachRuntime(started.lease, selected.sess)
		default:
			return errDelegateTargetBusy
		}
	}
	if restored {
		candidate := sub
		tracked, inserted, trackErr := owner.subagents.admitReconstructed(candidate, attach)
		if trackErr != nil {
			candidate.sess.discardRestoredCandidate()
			finishRestore(nil, trackErr)
			return nil, trackErr
		}
		if !inserted {
			candidate.sess.discardRestoredCandidate()
			sub = tracked
			restored = false
		}
	} else if err := attach(sub); err != nil {
		finishRestore(nil, err)
		return nil, err
	}
	if restored && owner.delegateRestoreBeforeSideEffects != nil {
		owner.delegateRestoreBeforeSideEffects(sub.sess)
	}
	bindDelegateActivityToOwner(sub.sess, s.delegateController, started.lease, owner)
	if restored && len(gates) != 0 {
		if err := gates[0].add(owner, sub, started); err != nil {
			finishRestore(nil, err)
			return nil, err
		}
	} else if restored {
		if err := sub.sess.runDeferredRestoreSideEffects(); err != nil {
			sub.sess.emit(warningDataFromError("restored delegate side effects incomplete", err))
		}
	}
	finishRestore(sub, nil)
	return sub, nil
}
func (s *Session) restoreColdDelegateOwnerRuntime(parentID string, gates ...*owedBootstrapRestore) (*Session, error) {
	if parentID == "" {
		return s, nil
	}
	if resident := s.delegateController.residentDelegateRuntime(parentID); resident != nil {
		return resident, nil
	}
	_, parent, err := s.restoreColdDelegateAttentionRuntime(parentID, gates...)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.sess == nil {
		return nil, errors.New("cold delegate attention parent runtime is unavailable")
	}
	return parent.sess, nil
}

func (s *Session) drivePendingDelegateAttention() bool {
	if s == nil || s.delegateController == nil || !s.isRootDelegateAttentionReceiver() {
		return false
	}
	s.forgetSettledDelegateAttentionWarnings()
	escalated := s.escalateUnreachableDelegateAttention()
	// Every wake this pass cannot drive says which guard predicate holds it
	// (#6330): a refusal here used to be silent, so a stranded lane was
	// indistinguishable from a quiet one.
	for _, refusal := range s.delegateController.pendingDelegateAttentionRefusals() {
		s.warnDelegateAttentionRefused(refusal.delegateID, refusal.predicate)
	}
	delegateID, _, pending := s.delegateController.selectDelegateAttentionWake()
	if !pending {
		return escalated
	}
	// The selection already took this pass's hold under the controller lock,
	// closing the selection-to-hold gap a release claim could slip through.
	// The defer releases it on every pass exit — reservation committed or
	// drive declined — so it can never pin the runtime warm, and overlapping
	// passes each keep their own reference.
	defer s.delegateController.releaseAttentionRestoreHold(delegateID)
	owner, sub, err := s.restoreColdDelegateAttentionRuntime(delegateID)
	if err != nil {
		s.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, delegateID, err)
		if errors.Is(err, errDelegateLaneHeld) {
			// The refusal happened before any launch ran, so no launch-side stamp
			// marks this delegate: a cold runtime in particular has no child to
			// stamp. Keep the owed row for the paced retry but out of the drain's
			// quiescence scans, which would otherwise wait on a lane another
			// session holds forever. A later restore that revalidates the lane
			// clears the quiet (reviveValidateIsolationLane).
			s.delegateController.quietLaneHeldAttention(delegateID)
		}
		s.countDelegateAttentionRestoreFailure(delegateID, err)
		s.scheduleDelegateAttentionRetry()
		return true
	}
	s.delegateController.delegateAttentionRestored(delegateID)
	s.delegateAttentionWarningResolved(delegateAttentionRestoreLabel, delegateID)
	if hook := s.cfg.testOnly.afterDelegateAttentionRestore; hook != nil {
		hook(delegateID, sub)
	}
	owner.driveDelegateAttention(sub)
	if s.delegateController.hasPendingDelegateAttention() {
		s.scheduleDelegateAttentionRetry()
	} else {
		s.resetDelegateAttentionRetry()
	}
	return true
}

// isTransientStartFailure reports whether a delegate start or restore failed
// for a reason that clears on its own: the target was busy (a race with a
// reservation, a stop or a reclamation), retirement had closed admission, or
// another session holds the delegate's isolation lane and may give it back.
// Those are retried as they stand. Any other failure is permanent for the
// purposes of giving up: the attention drive counts it toward handing the
// attention to the root.
func isTransientStartFailure(err error) bool {
	return errors.Is(err, errDelegateTargetBusy) ||
		errors.Is(err, ErrRetirementUnavailable) ||
		errors.Is(err, errDelegateLaneHeld)
}
