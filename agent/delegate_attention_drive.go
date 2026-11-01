package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
			sub.sess.scheduleNotificationRetry()
			return true
		}
	}
	if len(ids) == 0 && !s.delegateController.generationOwesSteering(sub.sess.owningDelegateID) {
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
	// Hold the child through reservation, acceptance, commit and launch. Common
	// wake callbacks can overlap that durable work, but must not admit another
	// executor before the run takes ownership under the same sub.mu hold.
	sub.mu.Lock()
	// childCommittedSendStart is consulted here, at the decision point, so every
	// path into the attention drive is gated at the depth the decision is made
	// rather than relying on each caller to remember (#940).
	// The exact receiver's callback reaches this primitive directly.
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
		sub.sess.deferNotificationAdmission()
		return true
	}
	launched := false
	var recheck bool
	defer func() {
		if recheck {
			s.delegateController.recheckNotificationReceivers()
		}
	}()
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
			if err := s.delegateController.requestSessionWake(sub.sess.id); err != nil {
				slog.Warn("notification wake failed", "session_id", sub.sess.id, "error", err)
			}
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
			sub.sess.scheduleNotificationRetry()
			return true
		}
		if len(pending) == 0 && !s.delegateController.generationOwesSteering(sub.sess.owningDelegateID) {
			if err := s.delegateController.clearResolvedDelegateAttention(sub.sess.owningDelegateID); err != nil {
				s.emit(warningDataFromError("clear resolved delegate attention", err))
			}
			return false
		}
		ids = pending
	}
	if handled, resumed := s.resumeYieldedDelegate(sub); handled {
		launched = resumed
		return true
	}
	if len(ids) == 0 {
		return false // Steering belongs to its open generation, not a successor.
	}
	reservation, err := s.delegateController.ReserveAttention(sub.sess, ids[0])
	if err != nil {
		if !errors.Is(err, errDelegateTargetBusy) && !errors.Is(err, errTreeAtCapacity) {
			s.emit(warningDataFromError("reserve delegate attention", err))
		}
		sub.sess.deferNotificationAdmission()
		return true
	}
	if err := sub.sess.acceptDelegateAttention(reservation); err != nil {
		s.emit(warningDataFromError("accept delegate attention", err))
		sub.sess.scheduleNotificationRetry()
		return true
	}
	started, err := s.delegateController.CommitStart(reservation)
	recheck = recheck || started.recheckNotifications
	if err != nil {
		s.emit(warningDataFromError("commit delegate attention", err))
		sub.sess.scheduleNotificationRetry()
		return true
	}
	s.delegateController.mu.Lock()
	s.delegateController.attentionDriveSeq++
	s.delegateController.attentionStateLocked(sub.sess.owningDelegateID).driveTurn = s.delegateController.attentionDriveSeq
	s.delegateController.mu.Unlock()
	if observer := s.cfg.testOnly.delegateAttentionStartCommitted; observer != nil {
		observer(sub)
	}
	s.delegateController.emitDelegateUpdate(started.plan)
	launchErr := s.launchAcceptedDelegateAttention(sub, started)
	launched = launchErr == nil
	if launchErr != nil {
		plans, finishErr := s.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(launchErr, "launch_failed"))
		recheck = recheck || plans.recheckNotifications
		plans.recheckNotifications = false
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
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return errors.New("delegate attention owner is closed")
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()
	runCtx, runCancel := context.WithTimeout(started.ctx, driveTurnTimeout)
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

func (s *Session) restoreColdDelegateAttentionRuntime(delegateID string, gates ...*owedBootstrapRestore) (*Session, *subagent, bool, error) {
	if s == nil || s.delegateController == nil || delegateID == "" {
		return nil, nil, false, errors.New("cold delegate attention restore identity is incomplete")
	}
	retirementRelease, retirementErr := s.beginRetirementMutation("delegate")
	if retirementErr != nil {
		return nil, nil, false, retirementErr
	}
	defer retirementRelease()
	// Hold the restore window from before the commit through installation, so a
	// lane handoff starting in this span refuses instead of tearing the lane down
	// under the runtime being installed.
	if err := s.delegateController.beginLaneRestore(delegateID); err != nil {
		return nil, nil, false, err
	}
	defer s.delegateController.endLaneRestore(delegateID)
	started, parentID, err := s.delegateController.idleDelegateRestoreCommit(delegateID)
	mode := delegateRuntimeAttachIdle
	if err != nil {
		started, parentID, err = s.delegateController.beginWaitingRuntimeRestore(delegateID)
		if err != nil {
			return nil, nil, false, fmt.Errorf("claim waiting runtime: %w", err)
		}
		mode = delegateRuntimeAttachWaiting
		defer s.delegateController.abandonWaitingRuntimeRestore(started.lease)
	}
	if observer := s.delegateRestoreAfterCommit; observer != nil {
		observer(delegateID)
	}
	owner, recheck, err := s.restoreColdDelegateOwnerRuntime(parentID, gates...)
	if err != nil {
		return nil, nil, recheck, err
	}

	sub, attached, err := s.reconstructDelegateAttentionRuntime(owner, started, mode, gates...)
	recheck = recheck || attached
	if err != nil {
		return nil, nil, recheck, fmt.Errorf("attach restored runtime: %w", err)
	}
	return owner, sub, recheck, err
}

func (s *Session) reconstructDelegateAttentionRuntime(owner *Session, started delegateStartCommit, mode delegateRuntimeAttachMode, gates ...*owedBootstrapRestore) (*subagent, bool, error) {
	var recheck bool
	runtime := delegateRuntime{owner: owner}
	sub, restored, finishRestore, err := runtime.restoreIdleForSend(started)
	finishRestoration := func(sub *subagent, restoreErr error) {
		released := finishRestore(sub, restoreErr)
		recheck = recheck || released
	}
	if err != nil {
		finishRestoration(nil, err)
		return nil, recheck, err
	}
	if sub == nil || sub.sess == nil {
		err = errors.New("delegate attention runtime is unavailable")
		finishRestoration(nil, err)
		return nil, recheck, err
	}
	// Before attachment the not-ready lease excludes callbacks installed by
	// restoreIdleForSend. Carry that exclusion across attachment with the same
	// drive guard used by send/attention, until the caller admits Stop or input.
	guarded, gateOwnsGuard := restored && len(gates) != 0 && gates[0].holdDrives, false
	guardedSub := sub
	if guarded {
		sub.mu.Lock()
		sub.driving = true
		sub.mu.Unlock()
		defer func() {
			if !gateOwnsGuard {
				releaseDriveGuard(guardedSub)
			}
		}()
	}
	var installation *delegateIdleRuntimeInstallation
	if mode == delegateRuntimeAttachIdle {
		installation, err = s.delegateController.beginIdleRuntimeInstallation(started.lease.delegateID)
		if err != nil {
			if restored {
				sub.sess.discardRestoredCandidate()
			}
			finishRestoration(nil, err)
			return nil, recheck, err
		}
		defer installation.release()
	}
	attach := func(selected *subagent) error {
		var released bool
		var attachErr error
		switch mode {
		case delegateRuntimeAttachIdle:
			released, attachErr = installation.attach(selected.sess)
		case delegateRuntimeAttachStarted:
			released, attachErr = s.delegateController.AttachRuntime(started.lease, selected.sess)
		case delegateRuntimeAttachWaiting:
			released, attachErr = s.delegateController.attachWaitingRuntime(started.lease, selected)
		default:
			attachErr = errDelegateTargetBusy
		}
		recheck = recheck || released
		return attachErr
	}
	if restored {
		candidate := sub
		tracked, inserted, trackErr := owner.subagents.admitReconstructed(candidate, attach)
		if trackErr != nil {
			candidate.sess.discardRestoredCandidate()
			finishRestoration(nil, trackErr)
			return nil, recheck, trackErr
		}
		if !inserted {
			candidate.sess.discardRestoredCandidate()
			sub = tracked
			restored = false
		}
	} else if err := attach(sub); err != nil {
		finishRestoration(nil, err)
		return nil, recheck, err
	}
	if restored && owner.delegateRestoreBeforeSideEffects != nil {
		owner.delegateRestoreBeforeSideEffects(sub.sess)
	}
	bindDelegateActivityToOwner(sub.sess, s.delegateController, started.lease, owner)
	if restored && len(gates) != 0 {
		if err := gates[0].add(owner, sub, started); err != nil {
			finishRestoration(nil, err)
			return nil, recheck, err
		}
		gateOwnsGuard = guarded
	} else if restored {
		if err := sub.sess.runDeferredRestoreSideEffects(); err != nil {
			sub.sess.emit(warningDataFromError("restored delegate side effects incomplete", err))
		}
	}
	finishRestoration(sub, nil)
	return sub, recheck, nil
}
func (s *Session) restoreColdDelegateOwnerRuntime(parentID string, gates ...*owedBootstrapRestore) (*Session, bool, error) {
	if parentID == "" {
		return s, false, nil
	}
	if resident := s.delegateController.residentDelegateRuntime(parentID); resident != nil {
		return resident, false, nil
	}
	_, parent, recheck, err := s.restoreColdDelegateAttentionRuntime(parentID, gates...)
	if err != nil {
		return nil, recheck, err
	}
	if parent == nil || parent.sess == nil {
		return nil, recheck, errors.New("cold delegate attention parent runtime is unavailable")
	}
	return parent.sess, recheck, nil
}
