package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

// takeSendDriveGuard takes sub's drive guard for a send's start: it sets
// driving unless the child can't take a new generation now (a run, a drive
// or a finalizer in flight, or worktree disposal holding it), and reports
// whether it did. The sibling guard in driveDelegateAttention routes
// through startBlockedLocked too, and also refuses a closed or fatally gated
// child and a committed-send claim, which a send has already settled by this
// point.
// `running` goes false at the top of a run's finalize block, before
// FinishGeneration moves the aggregate back to idle and before finalizing is
// cleared, so the finalizer and dispose flags are read under the same sub.mu
// hold as running and driving.
func (s *Session) takeSendDriveGuard(sub *subagent) bool {
	if observer := s.cfg.testOnly.delegateSendChildResolved; observer != nil {
		observer(sub)
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.startBlockedLocked() {
		return false
	}
	sub.driving = true
	return true
}

// releaseDriveGuard gives back a child's drive guard (sub.driving) that no run
// took over, and reports whether an attention drive was refused on the guard
// while it was held. An attention drive or notification turn that gets true
// re-drives the child (#3723). A send ignores the report: a send that fails
// re-drives the child in its rollback, and one that succeeds hands the child
// to a run that drains it.
func releaseDriveGuard(sub *subagent) bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.driving = false
	refused := sub.attentionDriveRefused
	sub.attentionDriveRefused = false
	return refused
}

// delegateFinalizationWaitCeiling bounds how long a send waits for a finished
// generation's finalize tail to release the delegate.
//
// TRIPWIRE: a hang guard only. The release is the tail's last local step
// (announce the result, then release), so it follows within milliseconds;
// this fires only when a tail never releases, and the send then refuses
// cleanly with target_busy.
const delegateFinalizationWaitCeiling = 10 * time.Second

// reserveStartAfterFinalization reserves a start for delegateID. A delegate
// whose finished generation is still finalizing is announced idle only as
// its finalize tail's last step, so while ReserveStart refuses for that
// reason alone the send waits for the release and reserves again, until it
// reserves, is refused for another reason, its context ends (the context's
// error), or delegateFinalizationWaitCeiling passes over the whole attempt
// (target_busy). A release that is ready wins over a context or ceiling that
// is ready at the same moment. This waits for admission, not for the
// delegate's reply, so max_wait_ms (which bounds the reply wait) doesn't
// govern it: a finalize tail releases within milliseconds.
func (s *Session) reserveStartAfterFinalization(ctx context.Context, actor delegateActor, delegateID string) (*delegateStartReservation, error) {
	reservation, err := s.delegateController.ReserveStart(actor, delegateID)
	finalizing, ok := errors.AsType[delegateFinalizingError](err)
	if !ok {
		return reservation, err
	}
	ceiling := delegateFinalizationWaitCeiling
	if override := s.cfg.testOnly.delegateFinalizationWaitCeiling; override != nil {
		ceiling = *override
	}
	timer := s.sclock().NewTimer(ceiling)
	defer timer.Stop()
	expired := false
	for ok {
		if !channelClosed(finalizing.released) {
			if expired {
				return nil, errDelegateTargetBusy
			}
			if observe := s.cfg.testOnly.delegateSendAwaitingFinalization; observe != nil {
				observe()
			}
			select {
			case <-finalizing.released:
			case <-ctx.Done():
				if !channelClosed(finalizing.released) {
					return nil, ctx.Err()
				}
			case <-timer.C():
				// The ceiling covers the whole attempt: it fires once.
				expired = true
				if !channelClosed(finalizing.released) {
					return nil, errDelegateTargetBusy
				}
			}
		}
		reservation, err = s.delegateController.ReserveStart(actor, delegateID)
		finalizing, ok = errors.AsType[delegateFinalizingError](err)
	}
	return reservation, err
}

// channelClosed reports whether ch is closed, without blocking. A nil channel
// is never closed.
func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// finishUnlaunchedGeneration fails a generation whose input was admitted but
// whose run never launched (launch_failed). No run finalizes its runtime, so
// the caller must release it (releaseFinishedGeneration) once the returned
// plans (which carry the generation's idle snapshot and result) have been
// executed, whether or not that failed, or the delegate stays refused as
// still finalizing.
func (c *delegateTreeController) finishUnlaunchedGeneration(lease delegateLease, cause error) (delegateMutationPlans, error) {
	return c.FinishGeneration(lease, delegatePermanentStartFailure(cause, "launch_failed"))
}

// releaseFinishedGeneration releases runtime's finished generation so the
// delegate takes its next start, warning if the report fails.
func (s *Session) releaseFinishedGeneration(lease delegateLease, runtime *Session) {
	if err := s.delegateController.ReportFinalizationQuiesced(lease, runtime); err != nil {
		s.emit(warningDataFromError("delegate finalization quiescence report failed", err))
	}
}

func (runtime delegateRuntime) send(ctx context.Context, delegateID, message string, maxWaitMS int) delegateSendOutcome {
	s := runtime.owner
	failed := func(err error) delegateSendOutcome {
		return delegateSendOutcome{result: sendMessageFailed(delegateID, err)}
	}
	if s == nil || s.delegateController == nil {
		return failed(errors.New("delegate controller is unavailable"))
	}
	retirementRelease, retirementErr := s.beginRetirementMutation("delegate")
	if retirementErr != nil {
		return failed(retirementErr)
	}
	defer retirementRelease()
	if delegateID == "" || message == "" {
		return failed(errors.New("invalid_request: delegate_id and message are required"))
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return failed(err)
	}
	if maxWaitMS > 0 {
		if observe := s.cfg.testOnly.delegateSendBeforePositiveWaitAdmission; observe != nil {
			observe()
		}
	}
	if maxWaitMS == 0 {
		if plans, steerErr := s.delegateController.Steer(ctx, actor, delegateID, message); steerErr == nil {
			_ = s.executeDelegateMutationPlans(plans)
			name := ""
			for _, update := range plans.updates {
				for _, row := range update.rows {
					if row.id == delegateID {
						name = row.descriptor.Name
						break
					}
				}
				if name != "" {
					break
				}
			}
			return delegateSendOutcome{result: sendMessageResult{
				Target:              delegateID,
				DelegateID:          delegateID,
				Type:                delegateResourceType,
				Status:              jobstore.StatusRunning,
				Name:                name,
				RunningInBackground: true,
				Action:              "steered",
			}}
		} else if !errors.Is(steerErr, errDelegateTargetBusy) {
			return failed(steerErr)
		}
	}
	reservation, err := s.reserveStartAfterFinalization(ctx, actor, delegateID)
	if err != nil {
		return failed(err)
	}
	// Claim the send-start window by CHILD SESSION ID, here, BEFORE the durable
	// commit rather than after it (#940). Claiming after CommitStart returned
	// left a window in which a wake-edge drive read the child as idle and could
	// launch a second, unleased turn on the session the commit was about to run;
	// the reservation already carries the child session id, so the claim can
	// precede the commit and that window does not exist. The claim is handed to
	// the run at the point `running` is set and released by the deferred rollback
	// on every exit that does not hand ownership over, including a failed
	// CommitStart below.
	committedChildID := strings.TrimSpace(reservation.descriptor.ChildSessionID)
	committedClaimHeld := false
	if committedChildID != "" {
		if !s.claimChildCommittedSendStart(committedChildID) {
			_ = s.delegateController.AbortStart(reservation)
			return failed(errDelegateTargetBusy)
		}
		committedClaimHeld = true
	}
	defer func() {
		if committedClaimHeld {
			s.releaseChildCommittedSendStart(committedChildID)
			// The claim refused every drive for the claimed child that landed
			// while it was held -- a child notification arriving mid-window runs
			// the child's notify, driveChildIfNotStopGated, which returns early on
			// the claim, and driveDelegateAttention refuses the stable
			// attention the same way -- so that wake is DROPPED. The run about to
			// launch would have drained the child's queue, but this exit did not
			// hand a run over (the claim is still held here), so re-drive the
			// claimed child's queued notifications, watch sends and armed stable
			// attention or the dropped wake can sit undriven forever. The re-drive
			// is scoped to committedChildID because the claim only gated that
			// child, so only that child can hold a wake it swallowed. The hand-off
			// path clears committedClaimHeld before this defer runs, so it never
			// re-drives: the handed-over run drains the queue itself, and
			// re-driving there would launch the second turn this claim prevents.
			// This runs from the deferred rollback after every failure exit
			// (aborted reservation, failed commit, failed restore, blocked
			// hand-off, start-input failure), and send holds no lock here.
			s.redriveLiveChild(committedChildID)
		}
	}()
	if observer := s.cfg.testOnly.delegateSendStartClaimed; observer != nil {
		observer(committedChildID)
	}
	// A resident child takes this generation only if it is free, so its
	// drive guard is taken before the commit: a busy child refuses the send
	// with nothing written. A cold child is restored from the committed start,
	// which owns the restore, so its guard is taken after the commit below.
	// guarded holds the guard until the run takes it over; every other exit
	// releases it here, before the claim's rollback re-drives the child.
	var guarded *subagent
	defer func() {
		if guarded != nil {
			releaseDriveGuard(guarded)
		}
	}()
	if committedChildID != "" {
		if resident := s.subagents.get(committedChildID); resident != nil && resident.sess != nil {
			if !s.takeSendDriveGuard(resident) {
				_ = s.delegateController.AbortStart(reservation)
				return failed(errDelegateTargetBusy)
			}
			guarded = resident
		}
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
	// Hold the drive guard for the committed-start window, exactly as
	// driveDelegateAttention does (#932/#940). CommitStart has already
	// consumed the reservation, but sub.running stays false through the restored
	// side effects and the start-input mutation plans (BeginStartInput,
	// preseedInput, CompleteStartInput); the run goroutine only sets running at
	// the far end of it. A wake-edge drive landing in that gap reads an idle
	// child, and driveChildIfNotStopGated falls through to
	// driveSubagentNotificationTurn, which launches a second, UNLEASED
	// EntryNotification turn on the session this generation is about to run. The
	// two turns then share one drain ladder and the queue pop (a destructive pop
	// with no owner check) lets the unleased turn steal the run's queued input.
	// sub.driving is the flag every other guard already reads as "a turn is in
	// flight on this idle child"; it is handed to the run under the same sub.mu
	// hold that sets running.
	//
	// A child the send didn't guard before the commit (restored cold, or a
	// resident replaced since) takes its guard here; the id-keyed
	// childCommittedSendStart claim covers it until then. Such a child is only
	// found busy if another path installed it first, and that start is
	// recorded as failed.
	if sub != guarded {
		if guarded != nil {
			releaseDriveGuard(guarded)
			guarded = nil
		}
		if !s.takeSendDriveGuard(sub) {
			cause := errDelegateTargetBusy
			return runtime.failStableSendStartAfterDispatch(ctx, started, delegateID, waiter, maxWaitMS, cause, func() {
				finishRestore(sub, nil)
			})
		}
		guarded = sub
	}
	bindDelegateActivity(sub.sess, s.delegateController, started.lease)
	if restored {
		if err := sub.sess.runDeferredRestoreSideEffects(); err != nil {
			s.emit(warningDataFromError("restored delegate side effects incomplete", err))
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
	if observer := s.cfg.testOnly.delegateSendStartCommitted; observer != nil {
		observer(sub)
	}
	claim, err := s.delegateController.BeginStartInput(started.lease)
	if err != nil {
		s.sendersWG.Done()
		return runtime.failStableSendStartAfterDispatch(ctx, started, delegateID, waiter, maxWaitMS, err, nil)
	}
	if err := runtime.preseedInput(sub.sess, message, started.transcriptPath, s.transcriptAuthor()); err != nil {
		plans, completeErr := s.delegateController.CompleteStartInput(claim, false, delegatePermanentStartFailure(err, "input_persist_failed"))
		s.sendersWG.Done()
		return runtime.stableSendFailureOutcomeAfterDispatch(ctx, started, waiter, maxWaitMS, plans, errors.Join(err, completeErr), nil)
	}
	plans, err := s.delegateController.CompleteStartInput(claim, true, delegateFinish{})
	if err != nil {
		s.sendersWG.Done()
		return runtime.stableSendFailureOutcomeAfterDispatch(ctx, started, waiter, maxWaitMS, plans, err, nil)
	}
	if err := s.executeDelegateMutationPlans(plans); err != nil {
		s.sendersWG.Done()
		failurePlans, finishErr := s.delegateController.finishUnlaunchedGeneration(started.lease, err)
		// The release runs once the failure is announced, whatever that did.
		return runtime.stableSendFailureOutcomeAfterDispatch(ctx, started, waiter, maxWaitMS, failurePlans, errors.Join(err, finishErr), func() {
			s.releaseFinishedGeneration(started.lease, sub.sess)
		})
	}
	runCtx, runCancel := context.WithCancel(started.ctx)
	runCtx = context.WithValue(runCtx, delegateRunLeaseContextKey{}, started.lease)
	runCtx = context.WithValue(runCtx, delegatePreseededInputContextKey{}, delegatePreseededInput{sessionID: sub.sess.id, input: message})
	sub.mu.Lock()
	sub.fatalRunGated = false
	resetSubagentForRunLocked(sub, runCancel, started.startedAt)
	// Hand the start claim over to the run under one hold, so the child never
	// reads idle between the committed start and the run that owns it.
	sub.driving = false
	sub.mu.Unlock()
	guarded = nil
	// running is now true under the same hold, so every drivability read refuses
	// on the run itself; the id-keyed claim has done its job and is released. The
	// deferred release sees committedClaimHeld false and does nothing.
	if committedClaimHeld {
		s.releaseChildCommittedSendStart(committedChildID)
		committedClaimHeld = false
	}
	s.launchSubagentRun(runCtx, sub, runCancel, message, descriptorProvenance(started.descriptor))
	s.startDelegateQuietWatchdog(started.ctx, started.lease)
	result := sendMessageResult{
		Target:              delegateID,
		DelegateID:          delegateID,
		Type:                delegateResourceType,
		Status:              jobstore.StatusRunning,
		AgentType:           started.descriptor.AgentType,
		Name:                started.descriptor.Name,
		Tools:               append([]string(nil), started.descriptor.ToolNameCeiling...),
		RunningInBackground: true,
		Action:              "started",
		TranscriptRef:       started.descriptor.TranscriptRef,
	}
	if waiter == nil {
		return delegateSendOutcome{result: result}
	}
	resolution := runtime.waitInline(ctx, waiter, maxWaitMS)
	if resolution.fallback || resolution.packet == nil || resolution.commit == nil {
		result.TimedOut = true
		return delegateSendOutcome{result: result}
	}
	completeDelegateSendResult(&result, *resolution.packet)
	return inlineOutcome(result, resolution)
}

// completeDelegateSendResult fills result as a send whose wait reached
// packet, a terminal result of its delegate.
func completeDelegateSendResult(result *sendMessageResult, packet delegatestore.TerminalPacket) {
	result.RunningInBackground = false
	result.Action = "completed"
	populateDelegateSendResult(result, packet)
}

func populateDelegateSendResult(result *sendMessageResult, packet delegatestore.TerminalPacket) {
	if result == nil {
		return
	}
	finish := delegatePreparedFinish(packet)
	result.Status = delegateOutcomeJobStatus(finish.outcome)
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
		if metadata.Name != "" {
			result.Name = metadata.Name
		}
		result.Task = metadata.Task
		result.Description = metadata.Description
		result.AgentType = metadata.AgentType
		result.Tools = append([]string(nil), metadata.Tools...)
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

func delegateOutcomeJobStatus(outcome delegatestore.OutcomeStatus) jobstore.Status {
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

// waitInline waits for the inline waiter to resolve, bounded by maxWaitMS when
// positive.
func (runtime delegateRuntime) waitInline(ctx context.Context, waiter *delegateInlineWaiter, maxWaitMS int) delegateInlineResolution {
	s := runtime.owner
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
	return s.delegateController.waitForDelegateInline(waitCtx, waiter)
}

// populateFailedSendResultFromPacket fills a failed start's result from a
// terminal packet that answered it. The durable outcome already seeded Status
// and Reason, and wins over what the packet's own report derives.
func populateFailedSendResultFromPacket(result *sendMessageResult, packet delegatestore.TerminalPacket) {
	durableStatus := result.Status
	durableReason := result.Reason
	populateDelegateSendResult(result, packet)
	if durableReason != "" {
		result.Reason = durableReason
	}
	if durableStatus != jobstore.StatusRunning {
		result.Status = durableStatus
	}
	result.Action = "completed"
	result.RunningInBackground = false
}

func (runtime delegateRuntime) failStableSendStartAfterDispatch(ctx context.Context, started delegateStartCommit, delegateID string, waiter *delegateInlineWaiter, maxWaitMS int, cause error, afterDispatch func()) delegateSendOutcome {
	plans, finishErr := runtime.owner.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(cause, "construction_failed"))
	return runtime.stableSendFailureOutcomeAfterDispatch(ctx, started, waiter, maxWaitMS, plans, errors.Join(cause, finishErr), afterDispatch)
}

func (runtime delegateRuntime) stableSendFailureOutcomeAfterDispatch(ctx context.Context, started delegateStartCommit, waiter *delegateInlineWaiter, maxWaitMS int, plans delegateMutationPlans, cause error, afterDispatch func()) delegateSendOutcome {
	executeErr := runtime.owner.executeDelegateMutationPlans(plans)
	if afterDispatch != nil {
		afterDispatch()
	}
	result := delegateFailedSendResult(started, plans, errors.Join(cause, executeErr))
	if waiter == nil || result.Action == "recovery_required" {
		return delegateSendOutcome{result: result}
	}
	if runtime.owner.delegateController.dropStoppedDelegateWaiter(started.lease) {
		// The failed start already settled this generation durably, and a
		// covering stop owns its delivery, so no inline delivery will answer
		// the wait: answer with the stopped outcome instead of sitting out
		// max_wait (#3502).
		return delegateSendOutcome{result: result}
	}
	resolution := runtime.waitInline(ctx, waiter, maxWaitMS)
	if resolution.fallback || resolution.packet == nil || resolution.commit == nil {
		result.TimedOut = true
		return delegateSendOutcome{result: result}
	}
	populateFailedSendResultFromPacket(&result, *resolution.packet)
	return inlineOutcome(result, resolution)
}

func delegateFailedSendResult(started delegateStartCommit, plans delegateMutationPlans, cause error) sendMessageResult {
	snapshot := latestDelegateMutationSnapshot(started.lease.delegateID, started.plan, plans)
	resumable := snapshot.resumable
	result := sendMessageResult{
		Target:              started.lease.delegateID,
		DelegateID:          started.lease.delegateID,
		Type:                delegateResourceType,
		Status:              jobstore.StatusRunning,
		AgentType:           started.descriptor.AgentType,
		Name:                started.descriptor.Name,
		Tools:               append([]string(nil), started.descriptor.ToolNameCeiling...),
		Resumable:           &resumable,
		RunningInBackground: false,
		Action:              "recovery_required",
		TranscriptRef:       started.descriptor.TranscriptRef,
		Err:                 cause,
	}
	if snapshot.lastOutcome != nil {
		result.Status = delegateOutcomeJobStatus(snapshot.lastOutcome.Status)
		result.Reason = snapshot.lastOutcome.Reason
		result.Action = "completed"
	}
	deliveryID := delegateDeliveryID(started.lease.delegateID, started.lease.generation)
	for _, delivery := range plans.deliveries {
		if delivery.delegateID == started.lease.delegateID && delivery.deliveryID == deliveryID {
			populateFailedSendResultFromPacket(&result, delivery.packet)
			break
		}
	}
	return result
}
