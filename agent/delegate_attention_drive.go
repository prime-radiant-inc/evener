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
	var ids []string
	if retryID := s.delegateController.reservedAttentionID(sub.sess); retryID != "" {
		ids = []string{retryID}
	} else {
		var err error
		ids, err = sub.sess.pendingDelegateAttentionIDs()
		if err != nil {
			s.emit(warningDataFromError("inspect delegate attention", err))
			return true
		}
	}
	if len(ids) == 0 {
		// The transcript holds no unresolved attention, but the controller may
		// still owe a wake whose resolution outran its run (a durable accept
		// followed by a declined commit). Reconcile the projection so the last
		// journal event is not left at needs_attention:true forever. This
		// reconciliation launches no run, so it never claims the wake: return
		// false and let the notification drive proceed as it did before.
		if err := s.delegateController.clearResolvedDelegateAttention(sub.sess.owningDelegateID); err != nil {
			s.emit(warningDataFromError("clear resolved delegate attention", err))
		}
		return false
	}
	if observer := s.cfg.testOnly.delegateAttentionBeforeDriveClaim; observer != nil {
		observer(sub)
	}
	// Claim the child for the WHOLE start, not just for this check. Everything
	// between here and launchAcceptedDelegateAttention is durable work
	// (ReserveAttention, acceptDelegateAttention's transcript append,
	// CommitStart, the delegate update emit), and the run goroutine only sets
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
	if blocked && sub.driving {
		sub.attentionDriveRefused = true
	}
	if !blocked {
		sub.driving = true
	}
	sub.mu.Unlock()
	if blocked {
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
	// The first read precedes the child claim. Another drive may consume and
	// finish that attention generation before this drive takes the claim, so
	// select again while the claim fences every competing drive. A reservation
	// already held for this runtime must still retry its exact accepted marker.
	if retryID := s.delegateController.reservedAttentionID(sub.sess); retryID != "" {
		ids = []string{retryID}
	} else {
		pending, err := sub.sess.pendingDelegateAttentionIDs()
		if err != nil {
			s.emit(warningDataFromError("inspect delegate attention", err))
			return true
		}
		if len(pending) == 0 {
			if err := s.delegateController.clearResolvedDelegateAttention(sub.sess.owningDelegateID); err != nil {
				s.emit(warningDataFromError("clear resolved delegate attention", err))
			}
			return false
		}
		ids = pending
	}
	reservation, err := s.delegateController.ReserveAttention(sub.sess, ids[0])
	if err != nil {
		if !errors.Is(err, errDelegateTargetBusy) && !errors.Is(err, errTreeAtCapacity) {
			s.emit(warningDataFromError("reserve delegate attention", err))
		}
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	if err := sub.sess.acceptDelegateAttention(reservation); err != nil {
		s.emit(warningDataFromError("accept delegate attention", err))
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	started, err := s.delegateController.CommitStart(reservation)
	if err != nil {
		s.emit(warningDataFromError("commit delegate attention", err))
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	if observer := s.cfg.testOnly.delegateAttentionStartCommitted; observer != nil {
		observer(sub)
	}
	s.delegateController.emitDelegateUpdate(started.plan)
	launchErr := s.launchAcceptedDelegateAttention(sub, started)
	launched = launchErr == nil
	if launchErr != nil {
		plans, finishErr := s.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(launchErr, "launch_failed"))
		if executeErr := s.executeDelegateMutationPlans(plans); finishErr == nil {
			finishErr = executeErr
		}
		s.emit(warningDataFromError("launch delegate attention", errors.Join(launchErr, finishErr)))
	}
	return true
}

func (s *Session) launchAcceptedDelegateAttention(sub *subagent, started delegateStartCommit) error {
	if s == nil || sub == nil || sub.sess == nil || s.delegateController == nil {
		return errDelegateDeliveryReceiverUnavailable
	}
	if started.descriptor.Isolation == "worktree" {
		// This launch is parent activity the orphan arm's evidence cannot see
		// on its own: record it before adopting the lane.
		s.refreshOwnActivityEvidence()
	}
	if err := s.reviveValidateIsolationLane(started); err != nil {
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
