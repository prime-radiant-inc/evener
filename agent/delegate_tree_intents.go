package agent

// Finish-path intent reducer.
//
// This file is the single authority for every decision on the generation
// finish path (spec: 2026-08-29 delegate lifecycle harness reducer design,
// Project 2). The exported and in-plan wrapper methods construct a
// finishIntent, call their site's reduce*Intent method, and — for the sites
// that carry a journal batch — drive the append and post-append effects
// through executeFinishDecisionLocked. Wrappers never branch on
// aggregate/live/controller state and never assign outcomes or dispositions;
// every guard evaluation lives in exactly one place here or in the shared
// predicates the reducers call (exactLeaseLocked, admitLeaseLocked,
// supervisionSuppressedLocked, finalizationReadyLocked,
// noActionBaseEligibleLocked, noActionEvidenceEligible).
//
// Contract for the reduce*Intent methods: the caller holds c.mu. The reducer
// acquires no locks, performs no journal I/O, and calls no Session methods
// (comparing runtime pointer identity for the binding-runtime guard is the
// one permitted use of a *Session). It performs the pre-append in-memory
// transitions (claim acquisition/fencing, evidence observation) and returns
// the decision: the journal batch to append, the append-failure latch
// target, the post-append effects (release claim, release generation with
// its delivery plans, evidence-version bump, snapshot capture), and the
// entry point's stale-lease policy already applied to the returned error.
//
// Deferred by spec (do not add here): the start-failure finishers
// (CompleteStartInput, FailCommittedStart, FailCommittedRestart,
// finishStoppedStartLocked) and the recovery finishers
// (reconcileRecoveryRequiredStopLocked,
// reconcileRuntimeLostFromEvidenceLocked).

import (
	"context"
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// finishStalePolicy is how an entry point surfaces a stale-lease guard
// failure. Three variants exist today: FinishGeneration suppresses it (empty
// plans, nil error), FinishNoAction propagates it verbatim, and
// ReportFinalizationQuiesced swallows it (nil error). The reducer applies the
// policy; wrappers only surface decision.err.
type finishStalePolicy uint8

const (
	finishStalePropagate finishStalePolicy = iota
	finishStaleSuppress
	finishStaleSwallow
)

// resolveStalePolicy applies this entry point's stale-lease policy to a guard
// error. It is the only classification of guard errors; wrappers never
// re-classify.
func (intent finishIntent) resolveStalePolicy(err error) error {
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		return err
	}
	if intent.stalePolicy == finishStaleSuppress || intent.stalePolicy == finishStaleSwallow {
		return nil
	}
	return err
}

// finishIntent is what a finish-path caller wants. It carries no controller
// state, only the request: the lease or claim the caller holds and the
// payload (finish, packet, run error, runtime identity). Each entry point
// passes its intent to its site's reduce*Intent method.
type finishIntent struct {
	lease   delegateLease
	claim   *delegateSettlementClaim
	mode    delegateSettlementMode
	finish  delegateFinish
	packet  *delegatestore.TerminalPacket
	runtime *Session
	// runErrorKnown/runErr bind the sampled run error to a settlement claim
	// (BeginRunFinalization).
	runErrorKnown bool
	runErr        error
	// authorizedNoAction marks the FinishNoAction entry into the generation
	// finish, whose no-action disposition was fenced by prepareNoAction.
	authorizedNoAction bool
	stalePolicy        finishStalePolicy
}

// finishDecision is the reducer's verdict for one intent.
type finishDecision struct {
	// err is the error to surface with the entry point's stale-lease policy
	// already applied. A nil err with nil events means "nothing to do, empty
	// success" (for example a suppressed stale lease or an ineligible
	// observation).
	err error
	// events is the journal batch to append before any parent-visible
	// effect. nil means this intent performs no append.
	events []delegatestore.Event
	// closure routes the append through appendResumabilityClosureLocked
	// (non-resumable exhaustion) and substitutes the closure plan for the
	// first post-append update.
	closure bool
	// lease is the generation the batch-carrying decision is about: the
	// closure append, the append-failure latch, the generation release, and
	// the snapshot capture all key off it.
	lease delegateLease
	// latchLive is the append-failure latch target: when the append fails,
	// the recovery triple (recoveryRequired, finalizationRecoveryRequired,
	// recoveryRunnerPending) is latched on latchLive when set (the generation
	// finish latches its authenticated live state unconditionally); otherwise
	// lease re-resolves the binding at apply time and latches only when it
	// still matches (CompleteSettlement).
	latchLive *delegateLiveState
	// Post-append effects, applied by finishEffectsLocked against post-append
	// durable state only when the append succeeded. Sites without a journal
	// batch apply their effects inline in the reducer instead.
	//
	// releaseClaim releases the settlement claim token
	// (releaseSettlementClaimLocked).
	releaseClaim *delegateSettlementClaim
	// releaseGeneration finishes the generation identified by lease: releases
	// its claims and stop tracking, releases the runtime binding (returning
	// the cancel that must run after c.mu is released), and captures the
	// snapshot and delivery plans (generationFinishedPlansLocked with
	// deliveryID).
	releaseGeneration bool
	deliveryID        string
	// bumpEvidence increments the evidence version.
	bumpEvidence bool
	// captureSnapshot appends the lease's delegate update plan.
	captureSnapshot bool
	// Outputs carried back through the unchanged wrapper signatures.
	claim          *delegateSettlementClaim
	continued      bool
	recorded       bool
	boundary       delegateSupervisionBoundary
	completion     delegateCompletionDecision
	finish         delegateFinish
	attentionPlans []delegateAttentionCleanupPlan
}

// executeFinishDecisionLocked drives the append, the append-failure recovery
// latch, and the post-append effects for a reducer decision that carries a
// journal batch (settlement completion and the ordinary and no-action
// generation finishes). The caller holds c.mu; the caller releases it and
// then runs the returned cancel after c.mu is released
// (cancel-after-unlock).
func (c *delegateTreeController) executeFinishDecisionLocked(decision finishDecision) (delegateMutationPlans, context.CancelFunc, error) {
	if decision.err != nil || decision.events == nil {
		return delegateMutationPlans{}, nil, decision.err
	}
	var appendErr error
	var closurePlan delegateUpdatePlan
	if decision.closure {
		closurePlan, appendErr = c.appendResumabilityClosureLocked(decision.lease.delegateID, decision.events...)
	} else {
		_, appendErr = c.appendLocked(decision.events...)
	}
	if appendErr != nil {
		if live := c.latchTargetLocked(decision); live != nil {
			live.recoveryRequired = true
			live.finalizationRecoveryRequired = true
			live.recoveryRunnerPending = true
		}
		return delegateMutationPlans{}, nil, appendErr
	}
	plans, cancel := c.finishEffectsLocked(decision, closurePlan)
	return plans, cancel, nil
}

// latchTargetLocked resolves the live state the recovery triple must be
// latched on after a failed finish append. The generation finish targets its
// authenticated live state unconditionally; CompleteSettlement re-resolves
// the binding at apply time (exactly as the pre-reducer code re-read c.live
// after the failed append) and latches only when it still matches the claim's
// lease. The caller holds c.mu.
func (c *delegateTreeController) latchTargetLocked(decision finishDecision) *delegateLiveState {
	if decision.latchLive != nil {
		return decision.latchLive
	}
	if live := c.live[decision.lease.delegateID]; live != nil && live.binding != nil && live.binding.lease == decision.lease {
		return live
	}
	return nil
}

// finishEffectsLocked applies the reducer's post-append effects against
// post-append durable state, using the existing pure plan helpers, and
// returns the concrete mutation plans plus the cancel that must run after
// c.mu is released. It branches only on the reducer's decision. The caller
// holds c.mu.
func (c *delegateTreeController) finishEffectsLocked(decision finishDecision, closurePlan delegateUpdatePlan) (delegateMutationPlans, context.CancelFunc) {
	plans := delegateMutationPlans{}
	var cancel context.CancelFunc
	if decision.releaseClaim != nil {
		c.releaseSettlementClaimLocked(decision.releaseClaim.token)
	}
	if decision.releaseGeneration {
		plans, cancel = c.generationFinishedPlansLocked(decision.lease, decision.deliveryID)
		if decision.closure {
			plans.updates[0] = closurePlan
		}
	}
	if decision.bumpEvidence {
		c.evidenceVersion++
	}
	if decision.captureSnapshot {
		plans.updates = append(plans.updates, c.capturedPlanLocked(decision.lease.delegateID))
	}
	return plans, cancel
}

// stopTracksFinalizationLocked reports whether a covering subtree stop is
// actively waiting on this lease and must hear about a finish-path effect that
// just applied. Single home for the active-and-covered membership check the
// recovery fences and claim acquisition consult.
func (c *delegateTreeController) stopTracksFinalizationLocked(lease delegateLease) bool {
	if c.stop == nil {
		return false
	}
	if _, active := c.stop.active[lease]; !active {
		return false
	}
	_, covered := c.stop.members[lease.delegateID]
	return covered
}

// reduceSupervisionBoundaryIntent is the supervision boundary.
func (c *delegateTreeController) reduceSupervisionBoundaryIntent(intent finishIntent) finishDecision {
	aggregate, live, err := c.exactLeaseLocked(intent.lease)
	if err != nil {
		return finishDecision{boundary: delegateSupervisionSuppress, err: intent.resolveStalePolicy(err)}
	}
	if c.supervisionSuppressedLocked(aggregate, live) {
		return finishDecision{boundary: delegateSupervisionSuppress}
	}
	if aggregate.Phase != delegatestore.PhaseRunning || !aggregate.Resumable || live.binding == nil || !live.binding.ready {
		return finishDecision{boundary: delegateSupervisionSuppress, err: errDelegateTargetBusy}
	}
	if len(live.pendingSteers) != 0 && intent.mode == delegateSettlementOrdinary {
		return finishDecision{boundary: delegateSupervisionContinue}
	}
	return finishDecision{boundary: delegateSupervisionProceed}
}

// reduceBeginFinalizationIntent acquires and fences the settlement claim:
// finalization phase admission, stop-promotion arbitration (ordinary mode
// under a covering stop that already ended the run promotes to terminal),
// pending-steer continuation decision, and quiet-claim join.
func (c *delegateTreeController) reduceBeginFinalizationIntent(intent finishIntent) finishDecision {
	lease := intent.lease
	mode := intent.mode
	var live *delegateLiveState
	switch mode {
	case delegateSettlementOrdinary:
		_, admitted, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
		if err != nil {
			exactAggregate, exact, exactErr := c.exactLeaseLocked(lease)
			if exactErr != nil {
				return finishDecision{err: exactErr}
			}
			if c.stop == nil {
				return finishDecision{err: err}
			}
			if exactAggregate.Phase != delegatestore.PhaseStopping || exactAggregate.PendingStopSeq != c.stop.requestSeq ||
				!c.stopTracksFinalizationLocked(lease) || exact.recoveryRequired || exact.binding == nil || !exact.binding.ready {
				return finishDecision{err: err}
			}
			admitted = exact
			mode = delegateSettlementTerminal
		}
		live = admitted
		if mode == delegateSettlementOrdinary && (len(live.pendingSteers) != 0 || c.hasSteeringClaimLocked(lease)) {
			return finishDecision{continued: true}
		}
	case delegateSettlementTerminal:
		aggregate, exact, err := c.exactLeaseLocked(lease)
		if err != nil {
			return finishDecision{err: err}
		}
		if aggregate.Phase != delegatestore.PhaseRunning && aggregate.Phase != delegatestore.PhaseStopping {
			return finishDecision{err: errDelegateTargetBusy}
		}
		if exact.recoveryRequired || exact.binding == nil || !exact.binding.ready {
			return finishDecision{err: errDelegateTargetBusy}
		}
		live = exact
	default:
		return finishDecision{err: errDelegateTargetBusy}
	}
	if c.hasSettlementClaimLocked(lease) {
		return finishDecision{err: errDelegateTargetBusy}
	}
	c.nextToken++
	var ready <-chan struct{}
	if live.quietClaim != nil {
		ready = live.quietClaim.done
	} else {
		closed := make(chan struct{})
		close(closed)
		ready = closed
	}
	claim := &delegateSettlementClaim{
		token:         c.nextToken,
		lease:         lease,
		mode:          mode,
		ready:         ready,
		runErrorKnown: intent.runErrorKnown,
		runErr:        intent.runErr,
	}
	c.settlementClaims[claim.token] = claim
	if c.stopTracksFinalizationLocked(lease) {
		c.stop.settlementClaims[claim.token] = struct{}{}
		c.signalStopProgressLocked()
	}
	c.evidenceVersion++
	return finishDecision{claim: claim}
}

// authenticateSettlementClaimLocked is the shared claim-taking prefix:
// exact-claim validation, the ready fence, and exact-lease authentication.
// Callers that dereference claim fields before authenticating keep their own
// claim-nil check first.
func (c *delegateTreeController) authenticateSettlementClaimLocked(claim *delegateSettlementClaim) (*delegatestore.Aggregate, *delegateLiveState, error) {
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return nil, nil, errDelegateStaleLease
	}
	if err := c.finalizationReadyLocked(claim); err != nil {
		return nil, nil, err
	}
	return c.exactLeaseLocked(claim.lease)
}

// reduceCompleteSettlementIntent completes an ordinary settlement claim.
func (c *delegateTreeController) reduceCompleteSettlementIntent(intent finishIntent) finishDecision {
	claim, supplied := intent.claim, intent.packet
	if claim == nil || claim.mode != delegateSettlementOrdinary || c.settlementClaims[claim.token] != claim {
		return finishDecision{err: errDelegateStaleLease}
	}
	// Third step is admitLeaseLocked (phase admission with closing/reclamation/
	// ancestor fences), not exactLeaseLocked — this site keeps its own chain so
	// guard order (and therefore error identity) is unchanged.
	if err := c.finalizationReadyLocked(claim); err != nil {
		return finishDecision{err: err}
	}
	aggregate, live, err := c.admitLeaseLocked(claim.lease, delegatestore.PhaseRunning)
	if err != nil {
		return finishDecision{err: err}
	}
	if aggregate.Trigger == delegatestore.TriggerAttention && len(live.attentionIDs) != 0 {
		return finishDecision{err: errDelegateTargetBusy}
	}
	packet := delegateMissingTerminalPacket()
	if supplied != nil {
		packet = cloneDelegateTerminalPacket(*supplied)
	}
	return finishDecision{
		events: []delegatestore.Event{{
			Kind:       delegatestore.EventDelegateTerminalPrepared,
			DelegateID: claim.lease.delegateID,
			TerminalPrepared: &delegatestore.TerminalPrepared{
				Generation: claim.lease.generation,
				Packet:     packet,
			},
		}},
		lease:           claim.lease,
		releaseClaim:    claim,
		bumpEvidence:    true,
		captureSnapshot: true,
	}
}

// reduceAttentionResolutionsIntent reads the attention cleanup plans an
// ordinary or terminal finalization must execute before the run's terminal
// state is published.
func (c *delegateTreeController) reduceAttentionResolutionsIntent(intent finishIntent) finishDecision {
	aggregate, live, err := c.authenticateSettlementClaimLocked(intent.claim)
	if err != nil {
		return finishDecision{err: err}
	}
	return finishDecision{attentionPlans: c.attentionResolutionPlansLocked(intent.claim.lease, aggregate, live)}
}

// noActionClaimState is the outcome of the shared no-action eligibility prefix:
// whether the claim is an ordinary claim with a known nil run error, and the
// authenticated aggregate/live pair behind it.
type noActionClaimState struct {
	ordinaryEligible bool
	aggregate        *delegatestore.Aggregate
	live             *delegateLiveState
}

// authenticateNoActionClaimLocked is the shared no-action eligibility prefix
// (claim identity, ordinary mode, known nil run error, ready fence, exact
// lease, phase, base eligibility, evidence eligibility). Sites differ only in
// the busy-vs-ineligible handling of the same outcomes, which readyErr carries
// back: errDelegateStaleLease and exactLease/finalization errors are hard
// failures for both; every other rejection is targetBusy-shaped and each
// caller decides whether it means "not eligible" (prepare) or "refuse"
// (finish). The phase check is caller-supplied because prepare admits only
// PhaseRunning while finish also admits PhaseStopping.
func (c *delegateTreeController) authenticateNoActionClaimLocked(claim *delegateSettlementClaim, allowStopping bool, readyErr error) (noActionClaimState, error) {
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return noActionClaimState{}, errDelegateStaleLease
	}
	if claim.mode != delegateSettlementOrdinary || !claim.runErrorKnown || claim.runErr != nil {
		return noActionClaimState{}, errDelegateTargetBusy
	}
	if readyErr != nil {
		return noActionClaimState{}, readyErr
	}
	aggregate, live, err := c.exactLeaseLocked(claim.lease)
	if err != nil {
		return noActionClaimState{}, err
	}
	phaseOK := aggregate.Phase == delegatestore.PhaseRunning || (allowStopping && aggregate.Phase == delegatestore.PhaseStopping)
	if !phaseOK || !c.noActionBaseEligibleLocked(aggregate, live) {
		return noActionClaimState{}, errDelegateTargetBusy
	}
	if !noActionEvidenceEligible(live.binding.evidence) {
		return noActionClaimState{}, errDelegateTargetBusy
	}
	return noActionClaimState{ordinaryEligible: true, aggregate: aggregate, live: live}, nil
}

// reducePrepareNoActionIntent binds the run's ordinary terminal fallback to
// the exact eligible attention claim before process-local terminal state is
// published. The claim stays live so only FinishNoAction can consume this
// authority.
func (c *delegateTreeController) reducePrepareNoActionIntent(intent finishIntent) finishDecision {
	claim := intent.claim
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return finishDecision{err: errDelegateStaleLease}
	}
	if err := c.finalizationReadyLocked(claim); err != nil {
		if errors.Is(err, errDelegateTargetBusy) {
			return finishDecision{}
		}
		return finishDecision{err: err}
	}
	state, err := c.authenticateNoActionClaimLocked(claim, false, nil)
	if errors.Is(err, errDelegateTargetBusy) {
		return finishDecision{}
	}
	if err != nil {
		return finishDecision{err: err}
	}
	evidence := state.live.binding.evidence
	retained := cloneDelegateFinish(intent.finish)
	evidence.fallback = &retained
	c.evidenceVersion++
	return finishDecision{recorded: true}
}

// reduceNoActionFinishIntent is the sole authority check for a packetless
// completed attention generation: exact ordinary claim, known nil run error,
// ready fence, no-action eligibility chain, and retained fallback.
func (c *delegateTreeController) reduceNoActionFinishIntent(intent finishIntent) finishDecision {
	claim := intent.claim
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return finishDecision{err: errDelegateStaleLease}
	}
	readyErr := c.finalizationReadyLocked(claim)
	state, err := c.authenticateNoActionClaimLocked(claim, true, readyErr)
	if err != nil {
		return finishDecision{err: err}
	}
	evidence := state.live.binding.evidence
	if evidence.fallback == nil {
		return finishDecision{err: errDelegateTargetBusy}
	}
	if state.aggregate.Phase == delegatestore.PhaseStopping {
		return finishDecision{finish: cloneDelegateFinish(*evidence.fallback)}
	}
	return finishDecision{finish: delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionCompletedNoAction,
		reason:      "attention_consumed_without_report",
		endedAt:     evidence.fallback.endedAt,
	}}
}

// reduceGenerationFinishIntent is the ordinary generation finish: the
// no-action authorization fence, finalization recovery fence, ready fence,
// attention-claim fencing, stop precedence and fallback selection,
// prepared-terminal interplay and outcome normalization, the exhaustion
// closure decision, and the RunFinished journal batch.
func (c *delegateTreeController) reduceGenerationFinishIntent(intent finishIntent) finishDecision {
	lease := intent.lease
	finish := intent.finish
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return finishDecision{err: intent.resolveStalePolicy(err)}
	}
	if !intent.authorizedNoAction && aggregate.Phase == delegatestore.PhaseRunning && finish.disposition == delegatestore.DispositionCompletedNoAction {
		return finishDecision{err: errDelegateTargetBusy}
	}
	if live.finalizationRecoveryRequired {
		return finishDecision{err: errDelegateTargetBusy}
	}
	if aggregate.Phase == delegatestore.PhaseRunning || aggregate.Phase == delegatestore.PhaseStopping {
		if err := c.finalizationReadyForLeaseLocked(lease, live); err != nil {
			return finishDecision{err: err}
		}
	}
	if aggregate.Phase != delegatestore.PhaseStopping && aggregate.Trigger == delegatestore.TriggerAttention && len(live.attentionIDs) != 0 {
		return finishDecision{err: errDelegateTargetBusy}
	}

	endedAt := finish.endedAt
	if endedAt.IsZero() {
		endedAt = c.now()
	}
	outcome := finish.outcome
	reason := finish.reason
	disposition := finish.disposition
	deliveryID := ""
	var events []delegatestore.Event

	switch aggregate.Phase {
	case delegatestore.PhaseSettling:
		if aggregate.PreparedTerminal == nil {
			return finishDecision{err: fmt.Errorf("delegate %q settling without prepared terminal", lease.delegateID)}
		}
		preparedFinish := delegatePreparedFinish(*aggregate.PreparedTerminal)
		outcome, disposition, reason = preparedFinish.outcome, preparedFinish.disposition, preparedFinish.reason
		if aggregate.PreparedTerminal.Kind == delegatestore.PacketTerminalError &&
			!delegateIsMissingTerminalPacket(*aggregate.PreparedTerminal) && finish.outcome != "" && finish.outcome != delegatestore.OutcomeCompleted {
			outcome = finish.outcome
			disposition = delegatestore.DispositionTerminalError
			reason = finish.reason
		} else if preparedFinish.outcome == delegatestore.OutcomeExhausted {
			finish = preparedFinish
		}
		deliveryID = delegateDeliveryID(lease.delegateID, lease.generation)
		finished := delegateRunFinishedEvent(lease, outcome, disposition, reason, endedAt, deliveryID, nil)
		events = []delegatestore.Event{finished}

	case delegatestore.PhaseStopping:
		// An externally cancelled generation still reports whatever evidence its
		// own run loop already gathered (task, worktree, scratch path — see
		// delegateTerminalPacketMetadata) via finish.packet; only fall back to the
		// bare synthetic packet when the run loop produced none at all (kata
		// tpb0). The fold layer (applyRunFinished) still has final say: it
		// replaces this with the bare packet when the owner is outside the
		// stopped subtree or the packet isn't a terminal-error kind.
		packet := delegateStoppedTerminalPacket()
		if finish.packet != nil {
			packet = cloneDelegateTerminalPacket(*finish.packet)
		}
		outcome = delegatestore.OutcomeStopped
		disposition = delegatestore.DispositionTerminalError
		reason = "stopped_by_parent"
		deliveryID = delegateDeliveryID(lease.delegateID, lease.generation)
		events = []delegatestore.Event{delegateRunFinishedEvent(lease, outcome, disposition, reason, endedAt, deliveryID, &packet)}

	case delegatestore.PhaseRunning:
		if disposition == delegatestore.DispositionCompletedNoAction {
			if outcome == "" {
				outcome = delegatestore.OutcomeCompleted
			}
			events = []delegatestore.Event{delegateRunFinishedEvent(lease, outcome, disposition, reason, endedAt, "", nil)}
			break
		}
		packet := delegateTerminalErrorPacket(reason)
		if finish.packet != nil {
			packet = cloneDelegateTerminalPacket(*finish.packet)
		}
		if outcome == "" {
			outcome = delegatestore.OutcomeFailed
		}
		if outcome == delegatestore.OutcomeCompleted && finish.packet == nil {
			outcome = delegatestore.OutcomeFailed
			reason = "missing_terminal"
			packet = delegateMissingTerminalPacket()
		}
		if disposition == "" {
			disposition = delegatePacketDisposition(packet)
		}
		deliveryID = delegateDeliveryID(lease.delegateID, lease.generation)
		events = []delegatestore.Event{
			{
				Kind:       delegatestore.EventDelegateTerminalPrepared,
				DelegateID: lease.delegateID,
				TerminalPrepared: &delegatestore.TerminalPrepared{
					Generation: lease.generation,
					Packet:     packet,
				},
			},
			delegateRunFinishedEvent(lease, outcome, disposition, reason, endedAt, deliveryID, nil),
		}

	default:
		return finishDecision{err: errDelegateTargetBusy}
	}
	events = delegateFinishMetadataEvents(events, lease, finish, outcome, reason)

	closure := outcome == delegatestore.OutcomeExhausted && finish.exhaustionResumable != nil && !*finish.exhaustionResumable
	return finishDecision{
		events:            events,
		closure:           closure,
		latchLive:         live,
		releaseGeneration: true,
		lease:             lease,
		deliveryID:        deliveryID,
	}
}

// reduceRequireFinalizationRecoveryIntent latches an exact finalization
// whose external attention persistence failed. The claim remains fenced until
// a durable stop can atomically close the open generation and then discard
// pending attention. This site performs no journal append; the latch is its
// effect, so it applies in-place on every successful reduction.
func (c *delegateTreeController) reduceRequireFinalizationRecoveryIntent(intent finishIntent) finishDecision {
	claim := intent.claim
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return finishDecision{err: errDelegateStaleLease}
	}
	_, live, err := c.exactLeaseLocked(claim.lease)
	if err != nil {
		return finishDecision{err: err}
	}
	live.recoveryRequired = true
	live.finalizationRecoveryRequired = true
	live.recoveryRunnerPending = true
	c.evidenceVersion++
	if c.stopTracksFinalizationLocked(claim.lease) {
		c.signalStopProgressLocked()
	}
	return finishDecision{}
}

// reduceReportQuiescedIntent releases only the process-local runner fence for
// the exact generation and resident runtime (pointer identity). Durable
// recovery authority remains latched until reconciliation closes or repairs
// that generation. A stale lease is swallowed.
func (c *delegateTreeController) reduceReportQuiescedIntent(intent finishIntent) finishDecision {
	_, live, err := c.exactLeaseLocked(intent.lease)
	if err != nil {
		return finishDecision{err: intent.resolveStalePolicy(err)}
	}
	if live.binding == nil || live.binding.runtime != intent.runtime {
		return finishDecision{err: errDelegateStaleLease}
	}
	if !live.recoveryRequired || !live.recoveryRunnerPending {
		return finishDecision{}
	}
	live.recoveryRunnerPending = false
	c.evidenceVersion++
	if c.stopTracksFinalizationLocked(intent.lease) {
		c.signalStopProgressLocked()
	}
	return finishDecision{}
}

// reduceWorkAdmittedIntent escalates the completion requirement when
// report-requiring work was admitted. Production start commits always attach
// evidence, but exact legacy/manual bindings may omit it and must still
// consume admitted steering — nil evidence is a tolerated no-op.
func (c *delegateTreeController) reduceWorkAdmittedIntent(intent finishIntent) finishDecision {
	_, live, err := c.exactLeaseLocked(intent.lease)
	if err != nil {
		return finishDecision{err: err}
	}
	evidence := live.binding.evidence
	if evidence == nil {
		return finishDecision{}
	}
	if evidence.requirement == delegateCompletionAttentionOnly {
		evidence.requirement = delegateCompletionReportRequired
		c.evidenceVersion++
	}
	return finishDecision{}
}

// reduceAttentionNoActionIntent records the bare attention no-action outcome
// when the requirement is still attention-only and no terminal was seen.
func (c *delegateTreeController) reduceAttentionNoActionIntent(intent finishIntent) finishDecision {
	evidence, err := c.completionEvidenceLocked(intent.lease)
	if err != nil {
		return finishDecision{err: err}
	}
	if evidence.requirement != delegateCompletionAttentionOnly || evidence.terminalSeen {
		return finishDecision{}
	}
	evidence.outcome = delegateCompletionOutcomeAttentionNoAction
	c.evidenceVersion++
	return finishDecision{recorded: true}
}

// reduceTerminalSeenIntent latches terminalSeen on the exact generation's
// evidence.
func (c *delegateTreeController) reduceTerminalSeenIntent(intent finishIntent) finishDecision {
	evidence, err := c.completionEvidenceLocked(intent.lease)
	if err != nil {
		return finishDecision{err: err}
	}
	if !evidence.terminalSeen {
		evidence.terminalSeen = true
		c.evidenceVersion++
	}
	return finishDecision{}
}

// reduceCompletionDecisionIntent is the completion decision in query form
// (pure read): useExistingTerminal / finishNoAction / needsNudge.
func (c *delegateTreeController) reduceCompletionDecisionIntent(intent finishIntent) finishDecision {
	evidence, err := c.completionEvidenceLocked(intent.lease)
	if err != nil {
		return finishDecision{completion: delegateCompletionNeedsNudge, err: err}
	}
	if evidence.terminalSeen {
		return finishDecision{completion: delegateCompletionUseExistingTerminal}
	}
	if evidence.requirement == delegateCompletionAttentionOnly && evidence.outcome == delegateCompletionOutcomeAttentionNoAction {
		return finishDecision{completion: delegateCompletionFinishNoAction}
	}
	return finishDecision{completion: delegateCompletionNeedsNudge}
}
