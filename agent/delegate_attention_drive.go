package agent

import (
	"context"
	"errors"
	"slices"

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
	ids, err := sub.sess.automaticDelegateAttentionIDs()
	if err != nil {
		s.emit(warningDataFromError("inspect delegate attention", err))
		return true
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
	// (ReserveAttention, the fold read behind prepareAttentionStart, CommitStart,
	// the delegate update emit), and the run goroutine only sets running at the
	// far end of it. Without the claim a wake-edge drive landing in that gap
	// reads an idle child and starts a second, UNLEASED turn on the session this
	// generation is about to run. The two turns then share one drain ladder and
	// the unleased one can pop the run's follow-up, leaving the generation to
	// settle attention-only instead of report-required. The claim is the drive
	// flag every other guard already reads, and it is handed over under the same
	// sub.mu hold that sets running.
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
	// consume that attention before this drive takes the claim, so select again
	// while the claim fences every competing drive.
	pending, err := sub.sess.automaticDelegateAttentionIDs()
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
	trigger := pending[0]
	reservation, err := s.delegateController.ReserveAttention(sub.sess, trigger)
	if err != nil {
		if !errors.Is(err, errDelegateTargetBusy) && !errors.Is(err, errTreeAtCapacity) {
			s.emit(warningDataFromError("reserve delegate attention", err))
		}
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	// Admission is what makes a concurrent stop or resumability closure wait
	// for this start's commit instead of settling the row under it. It writes
	// nothing to the transcript: the consume marker lands only when the launched
	// turn presents the row (finishAttentionTurn).
	if _, _, err := s.delegateController.attentionReservationIdentity(reservation, sub.sess); err != nil {
		_ = s.delegateController.AbortStart(reservation)
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	// The row the generation presents must still be pending from the far side
	// of the admission. A row resolved in this window — a stop's discard, a
	// conflicting resolution — leaves nothing to present, so release the
	// reservation and decline rather than committing a generation to a row no
	// request will carry.
	pending, err = sub.sess.automaticDelegateAttentionIDs()
	if err != nil {
		_ = s.delegateController.AbortStart(reservation)
		s.emit(warningDataFromError("inspect delegate attention", err))
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	if !slices.Contains(pending, trigger) {
		_ = s.delegateController.AbortStart(reservation)
		if err := s.delegateController.clearResolvedDelegateAttention(sub.sess.owningDelegateID); err != nil {
			s.emit(warningDataFromError("clear resolved delegate attention", err))
		}
		return false
	}
	if err := s.delegateController.prepareAttentionStart(reservation, sub.sess, pending); err != nil {
		_ = s.delegateController.AbortStart(reservation)
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
		// controller refused. The refusal writes nothing, so the reservation is
		// released here exactly as CommitStart releases its own refusals.
		if commitErr = fault(); commitErr != nil {
			_ = s.delegateController.AbortStart(reservation)
		}
	}
	var started delegateStartCommit
	if commitErr == nil {
		started, commitErr = s.delegateController.CommitStart(reservation)
	}
	if commitErr != nil {
		s.emit(warningDataFromError("commit delegate attention", commitErr))
		s.delegateController.retryDelegateAttentionLater()
		return true
	}
	if observer := s.cfg.testOnly.delegateAttentionStartCommitted; observer != nil {
		observer(sub)
	}
	// Latch the trigger this committed generation will present. The latch is
	// in-memory only: it names the run's row for the settle tail, which holds
	// that row out of automatic drives when a stop leaves it unpresented. The
	// request that presents it clears the latch.
	sub.sess.holdAcceptedTrigger(trigger)
	s.delegateController.emitDelegateUpdate(started.plan)
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
		// The launch refused before any request ran, so nothing consumed the
		// row: it is still pending in the transcript and the commit's wake set
		// still carries it. The paced retry drives it again, and a row a later
		// event resolves is seen resolved by that retry's own fold read.
		sub.sess.clearAcceptedTrigger()
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
	if restored {
		// The idle release may have reclaimed the runtime that held a user
		// stop's quiet in memory; a freshly built session rebuilds that
		// quiet from the durable outcome and the fold BEFORE the candidate
		// joins the tree, so a drive landing in the admit-to-attach window
		// below reads the automatic list with the hold already in place
		// instead of presenting the stopped reopen riding the stale armed
		// wake. The candidate is disposable — a resident winner discards it,
		// derived holds and all — so a pass that only found the runtime
		// resident never rebuilds a hold on quiet its in-memory state never
		// lost. The call is a no-op for every restored runtime that owes no
		// such stop.
		sub.sess.deriveHeldDelegateAttentionFromOutcome()
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
