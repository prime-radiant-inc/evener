package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

const delegateFinishReasonLimit = 512

type delegateSupervisionBoundary uint8

const (
	delegateSupervisionProceed delegateSupervisionBoundary = iota
	delegateSupervisionContinue
	delegateSupervisionSuppress
)

type delegateSettlementMode uint8

const (
	delegateSettlementOrdinary delegateSettlementMode = iota
	delegateSettlementTerminal
)

type delegateSettlementClaim struct {
	token         uint64
	lease         delegateLease
	mode          delegateSettlementMode
	ready         <-chan struct{}
	runErrorKnown bool
	runErr        error
}

// SupervisionBoundary linearizes pending-steer and stop precedence before
// ordinary nudge and hook work begins outside the controller mutex.
func (c *delegateTreeController) SupervisionBoundary(lease delegateLease, mode delegateSettlementMode) (delegateSupervisionBoundary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return delegateSupervisionSuppress, err
	}
	if c.supervisionSuppressedLocked(aggregate, live) {
		return delegateSupervisionSuppress, nil
	}
	if aggregate.Phase != delegatestore.PhaseRunning || !aggregate.Resumable || live.binding == nil || !live.binding.ready {
		return delegateSupervisionSuppress, errDelegateTargetBusy
	}
	if len(live.pendingSteers) != 0 && mode == delegateSettlementOrdinary {
		return delegateSupervisionContinue, nil
	}
	return delegateSupervisionProceed, nil
}

// supervisionSuppressedLocked reports whether ordinary supervision must be
// suppressed because the controller is closing, the generation is stopping or
// stop-pending, or its live state needs recovery. It is the single
// suppression predicate behind SupervisionBoundary's suppress-without-cause
// branch; the not-running/not-ready path (errDelegateTargetBusy) is a
// different case and deliberately not covered here. Caller holds c.mu.
func (c *delegateTreeController) supervisionSuppressedLocked(aggregate *delegatestore.Aggregate, live *delegateLiveState) bool {
	return c.closing || aggregate.Phase == delegatestore.PhaseStopping || aggregate.PendingStopSeq != 0 || live.recoveryRequired
}

// BeginSettlement makes the final pending-steer decision and fences new work
// while the runtime performs its last cleanup and samples terminal evidence.
func (c *delegateTreeController) BeginSettlement(lease delegateLease) (*delegateSettlementClaim, bool, error) {
	return c.BeginFinalization(lease, delegateSettlementOrdinary)
}

// BeginFinalization fences new runtime work and joins any quiet-attention write
// that was already admitted for the exact generation. Only ordinary
// finalization arbitrates pending steering.
func (c *delegateTreeController) BeginFinalization(lease delegateLease, mode delegateSettlementMode) (*delegateSettlementClaim, bool, error) {
	return c.beginFinalization(lease, mode, false, nil)
}

// BeginRunFinalization binds the sampled run error to the exact settlement
// claim. Packetless no-action authority requires this fact to be known and nil;
// generic controller callers cannot obtain that authority through a claim whose
// run result was not supplied.
func (c *delegateTreeController) BeginRunFinalization(lease delegateLease, mode delegateSettlementMode, runErr error) (*delegateSettlementClaim, bool, error) {
	return c.beginFinalization(lease, mode, true, runErr)
}

func (c *delegateTreeController) beginFinalization(lease delegateLease, mode delegateSettlementMode, runErrorKnown bool, runErr error) (*delegateSettlementClaim, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var live *delegateLiveState
	switch mode {
	case delegateSettlementOrdinary:
		_, admitted, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
		if err != nil {
			exactAggregate, exact, exactErr := c.exactLeaseLocked(lease)
			if exactErr != nil {
				return nil, false, exactErr
			}
			if c.stop == nil {
				return nil, false, err
			}
			_, active := c.stop.active[lease]
			_, covered := c.stop.members[lease.delegateID]
			if exactAggregate.Phase != delegatestore.PhaseStopping || exactAggregate.PendingStopSeq != c.stop.requestSeq ||
				!active || !covered || exact.recoveryRequired || exact.binding == nil || !exact.binding.ready {
				return nil, false, err
			}
			admitted = exact
			mode = delegateSettlementTerminal
		}
		live = admitted
		if mode == delegateSettlementOrdinary && (len(live.pendingSteers) != 0 || c.hasSteeringClaimLocked(lease)) {
			return nil, true, nil
		}
	case delegateSettlementTerminal:
		aggregate, exact, err := c.exactLeaseLocked(lease)
		if err != nil {
			return nil, false, err
		}
		if aggregate.Phase != delegatestore.PhaseRunning && aggregate.Phase != delegatestore.PhaseStopping {
			return nil, false, errDelegateTargetBusy
		}
		if exact.recoveryRequired || exact.binding == nil || !exact.binding.ready {
			return nil, false, errDelegateTargetBusy
		}
		live = exact
	default:
		return nil, false, errDelegateTargetBusy
	}
	if c.hasSettlementClaimLocked(lease) {
		return nil, false, errDelegateTargetBusy
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
		runErrorKnown: runErrorKnown,
		runErr:        runErr,
	}
	c.settlementClaims[claim.token] = claim
	if c.stop != nil {
		if _, active := c.stop.active[lease]; active {
			if _, covered := c.stop.members[lease.delegateID]; covered {
				c.stop.settlementClaims[claim.token] = struct{}{}
				c.signalStopProgressLocked()
			}
		}
	}
	c.evidenceVersion++
	return claim, false, nil
}

func (c *delegateTreeController) CompleteSettlement(claim *delegateSettlementClaim, supplied *delegatestore.TerminalPacket) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || claim.mode != delegateSettlementOrdinary || c.settlementClaims[claim.token] != claim {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	if err := c.finalizationReadyLocked(claim); err != nil {
		return delegateMutationPlans{}, err
	}
	aggregate, live, err := c.admitLeaseLocked(claim.lease, delegatestore.PhaseRunning)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	if aggregate.Trigger == delegatestore.TriggerAttention && len(live.attentionIDs) != 0 {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	packet := delegateMissingTerminalPacket()
	if supplied != nil {
		packet = cloneDelegateTerminalPacket(*supplied)
	}
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateTerminalPrepared,
		DelegateID: claim.lease.delegateID,
		TerminalPrepared: &delegatestore.TerminalPrepared{
			Generation: claim.lease.generation,
			Packet:     packet,
		},
	}); err != nil {
		if live := c.live[claim.lease.delegateID]; live != nil && live.binding != nil && live.binding.lease == claim.lease {
			live.recoveryRequired = true
			live.finalizationRecoveryRequired = true
			live.recoveryRunnerPending = true
		}
		return delegateMutationPlans{}, err
	}
	c.releaseSettlementClaimLocked(claim.token)
	c.evidenceVersion++
	plan := c.capturedPlanLocked(claim.lease.delegateID)
	return delegateMutationPlans{updates: []delegateUpdatePlan{plan}}, nil
}

func (c *delegateTreeController) AttentionResolutionsForFinalization(claim *delegateSettlementClaim) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	if err := c.finalizationReadyLocked(claim); err != nil {
		return delegateMutationPlans{}, err
	}
	aggregate, live, err := c.exactLeaseLocked(claim.lease)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	return delegateMutationPlans{
		attention:             c.attentionResolutionPlansLocked(claim.lease, aggregate, live),
		attentionFinalization: claim,
	}, nil
}

// prepareNoAction binds the run's ordinary terminal fallback to the exact
// eligible attention claim before process-local terminal state is published.
// The claim stays live so only FinishNoAction can consume this authority.
func (c *delegateTreeController) prepareNoAction(claim *delegateSettlementClaim, fallback delegateFinish) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return false, errDelegateStaleLease
	}
	if claim.mode != delegateSettlementOrdinary {
		return false, nil
	}
	if !claim.runErrorKnown || claim.runErr != nil {
		return false, nil
	}
	if err := c.finalizationReadyLocked(claim); err != nil {
		if errors.Is(err, errDelegateTargetBusy) {
			return false, nil
		}
		return false, err
	}
	aggregate, live, err := c.exactLeaseLocked(claim.lease)
	if err != nil {
		return false, err
	}
	if aggregate.Phase != delegatestore.PhaseRunning || !c.noActionBaseEligibleLocked(aggregate, live) {
		return false, nil
	}
	evidence := live.binding.evidence
	if !noActionEvidenceEligible(evidence) {
		return false, nil
	}
	retained := cloneDelegateFinish(fallback)
	evidence.fallback = &retained
	c.evidenceVersion++
	return true, nil
}

func (c *delegateTreeController) noActionBaseEligibleLocked(aggregate *delegatestore.Aggregate, live *delegateLiveState) bool {
	return aggregate.Trigger == delegatestore.TriggerAttention && aggregate.PreparedTerminal == nil &&
		!live.recoveryRequired && live.binding.ready && len(live.attentionIDs) == 0
}

func noActionEvidenceEligible(evidence *delegateGenerationEvidence) bool {
	return evidence != nil && evidence.requirement == delegateCompletionAttentionOnly &&
		evidence.outcome == delegateCompletionOutcomeAttentionNoAction && !evidence.terminalSeen
}

// RequireFinalizationRecovery latches an exact finalization whose external
// attention persistence failed. The claim remains fenced until a durable stop
// can atomically close the open generation and then discard pending attention.
func (c *delegateTreeController) RequireFinalizationRecovery(claim *delegateSettlementClaim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return errDelegateStaleLease
	}
	_, live, err := c.exactLeaseLocked(claim.lease)
	if err != nil {
		return err
	}
	if !live.recoveryRequired {
		live.recoveryRequired = true
	}
	live.finalizationRecoveryRequired = true
	live.recoveryRunnerPending = true
	c.evidenceVersion++
	if c.stop != nil {
		if _, active := c.stop.active[claim.lease]; active {
			if _, covered := c.stop.members[claim.lease.delegateID]; covered {
				c.signalStopProgressLocked()
			}
		}
	}
	return nil
}

// ReportFinalizationQuiesced releases only the process-local runner fence for
// the exact generation and resident runtime. Durable recovery authority remains
// latched until reconciliation closes or repairs that generation.
func (c *delegateTreeController) ReportFinalizationQuiesced(lease delegateLease, runtime *Session) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		if errors.Is(err, errDelegateStaleLease) {
			return nil
		}
		return err
	}
	if live.binding == nil || live.binding.runtime != runtime {
		return errDelegateStaleLease
	}
	if !live.recoveryRequired || !live.recoveryRunnerPending {
		return nil
	}
	live.recoveryRunnerPending = false
	c.evidenceVersion++
	if c.stop != nil {
		if _, active := c.stop.active[lease]; active {
			if _, covered := c.stop.members[lease.delegateID]; covered {
				c.signalStopProgressLocked()
			}
		}
	}
	return nil
}

func (c *delegateTreeController) attentionResolutionPlansLocked(lease delegateLease, aggregate *delegatestore.Aggregate, live *delegateLiveState) []delegateAttentionCleanupPlan {
	if aggregate == nil || aggregate.Phase == delegatestore.PhaseStopping || aggregate.Trigger != delegatestore.TriggerAttention || live == nil || len(live.attentionIDs) == 0 {
		return nil
	}
	plans := make([]delegateAttentionCleanupPlan, 0, len(live.attentionIDs))
	for _, attentionID := range live.attentionIDs {
		plans = append(plans, delegateAttentionCleanupPlan{
			lease:         lease,
			delegateID:    lease.delegateID,
			transcriptRef: aggregate.Descriptor.TranscriptRef,
			attentionID:   attentionID,
			disposition:   delegateAttentionConsumed,
			runtime:       live.runtime,
		})
	}
	return plans
}

func (c *delegateTreeController) finalizationReadyLocked(claim *delegateSettlementClaim) error {
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return errDelegateStaleLease
	}
	select {
	case <-claim.ready:
	default:
		return errDelegateTargetBusy
	}
	live := c.live[claim.lease.delegateID]
	if live == nil || live.binding == nil || live.binding.lease != claim.lease {
		return errDelegateStaleLease
	}
	if live.quietClaim != nil {
		return errDelegateTargetBusy
	}
	return nil
}

func (c *delegateTreeController) finalizationReadyForLeaseLocked(lease delegateLease, live *delegateLiveState) error {
	if live != nil && live.quietClaim != nil {
		return errDelegateTargetBusy
	}
	for _, claim := range c.settlementClaims {
		if claim != nil && claim.lease == lease {
			return c.finalizationReadyLocked(claim)
		}
	}
	return nil
}

func (c *delegateTreeController) releaseSettlementClaimLocked(token uint64) {
	delete(c.settlementClaims, token)
	if c.stop != nil {
		if _, tracked := c.stop.settlementClaims[token]; tracked {
			delete(c.stop.settlementClaims, token)
			c.signalStopProgressLocked()
		}
	}
}

func (c *delegateTreeController) releaseSettlementClaimsForLeaseLocked(lease delegateLease) {
	for token, claim := range c.settlementClaims {
		if claim != nil && claim.lease == lease {
			c.releaseSettlementClaimLocked(token)
		}
	}
}

func (c *delegateTreeController) hasSettlementClaimLocked(lease delegateLease) bool {
	for _, claim := range c.settlementClaims {
		if claim != nil && claim.lease == lease {
			return true
		}
	}
	return false
}

func (c *delegateTreeController) hasSteeringClaimLocked(lease delegateLease) bool {
	for _, claim := range c.steeringClaims {
		if claim != nil && claim.lease == lease {
			return true
		}
	}
	return false
}

func (c *delegateTreeController) FinishGeneration(lease delegateLease, finish delegateFinish) (delegateMutationPlans, error) {
	c.mu.Lock()
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		c.mu.Unlock()
		if errors.Is(err, errDelegateStaleLease) {
			return delegateMutationPlans{}, nil
		}
		return delegateMutationPlans{}, err
	}
	plans, cancel, err := c.finishGenerationLocked(lease, finish, aggregate, live, false)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return plans, err
}

// FinishNoAction is the sole authority for a packetless completed attention
// generation. Its exact ordinary claim and retained fallback were fenced by
// prepareNoAction before process-local terminal state publication.
func (c *delegateTreeController) FinishNoAction(claim *delegateSettlementClaim) (delegateMutationPlans, error) {
	c.mu.Lock()
	aggregate, live, finish, err := c.noActionFinishLocked(claim)
	if err != nil {
		c.mu.Unlock()
		return delegateMutationPlans{}, err
	}
	plans, cancel, err := c.finishGenerationLocked(claim.lease, finish, aggregate, live, true)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return plans, err
}

func (c *delegateTreeController) finishGenerationLocked(lease delegateLease, finish delegateFinish, aggregate *delegatestore.Aggregate, live *delegateLiveState, authorizedNoAction bool) (delegateMutationPlans, context.CancelFunc, error) {
	if !authorizedNoAction && aggregate.Phase == delegatestore.PhaseRunning && finish.disposition == delegatestore.DispositionCompletedNoAction {
		return delegateMutationPlans{}, nil, errDelegateTargetBusy
	}
	if live.finalizationRecoveryRequired {
		return delegateMutationPlans{}, nil, errDelegateTargetBusy
	}
	if aggregate.Phase == delegatestore.PhaseRunning || aggregate.Phase == delegatestore.PhaseStopping {
		if err := c.finalizationReadyForLeaseLocked(lease, live); err != nil {
			return delegateMutationPlans{}, nil, err
		}
	}
	if aggregate.Phase != delegatestore.PhaseStopping && aggregate.Trigger == delegatestore.TriggerAttention && len(live.attentionIDs) != 0 {
		return delegateMutationPlans{}, nil, errDelegateTargetBusy
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
			return delegateMutationPlans{}, nil, fmt.Errorf("delegate %q settling without prepared terminal", lease.delegateID)
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
		return delegateMutationPlans{}, nil, errDelegateTargetBusy
	}
	events = delegateFinishMetadataEvents(events, lease, finish, outcome, reason)

	closure := outcome == delegatestore.OutcomeExhausted && finish.exhaustionResumable != nil && !*finish.exhaustionResumable
	var closurePlan delegateUpdatePlan
	var appendErr error
	if closure {
		closurePlan, appendErr = c.appendResumabilityClosureLocked(lease.delegateID, events...)
	} else {
		_, appendErr = c.appendLocked(events...)
	}
	if appendErr != nil {
		live.recoveryRequired = true
		live.finalizationRecoveryRequired = true
		live.recoveryRunnerPending = true
		return delegateMutationPlans{}, nil, appendErr
	}
	plans, generationCancel := c.generationFinishedPlansLocked(lease, deliveryID)
	if closure {
		plans.updates[0] = closurePlan
	}
	return plans, generationCancel, nil
}

func (c *delegateTreeController) noActionFinishLocked(claim *delegateSettlementClaim) (*delegatestore.Aggregate, *delegateLiveState, delegateFinish, error) {
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return nil, nil, delegateFinish{}, errDelegateStaleLease
	}
	if claim.mode != delegateSettlementOrdinary {
		return nil, nil, delegateFinish{}, errDelegateTargetBusy
	}
	if !claim.runErrorKnown || claim.runErr != nil {
		return nil, nil, delegateFinish{}, errDelegateTargetBusy
	}
	if err := c.finalizationReadyLocked(claim); err != nil {
		return nil, nil, delegateFinish{}, err
	}
	aggregate, live, err := c.exactLeaseLocked(claim.lease)
	if err != nil {
		return nil, nil, delegateFinish{}, err
	}
	if (aggregate.Phase != delegatestore.PhaseRunning && aggregate.Phase != delegatestore.PhaseStopping) ||
		!c.noActionBaseEligibleLocked(aggregate, live) {
		return nil, nil, delegateFinish{}, errDelegateTargetBusy
	}
	evidence := live.binding.evidence
	if !noActionEvidenceEligible(evidence) || evidence.fallback == nil {
		return nil, nil, delegateFinish{}, errDelegateTargetBusy
	}
	if aggregate.Phase == delegatestore.PhaseStopping {
		return aggregate, live, cloneDelegateFinish(*evidence.fallback), nil
	}
	return aggregate, live, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionCompletedNoAction,
		reason:      "attention_consumed_without_report",
		endedAt:     evidence.fallback.endedAt,
	}, nil
}

func cloneDelegateFinish(finish delegateFinish) delegateFinish {
	cloned := finish
	if finish.exhaustionResumable != nil {
		resumable := *finish.exhaustionResumable
		cloned.exhaustionResumable = &resumable
	}
	if finish.packet != nil {
		packet := cloneDelegateTerminalPacket(*finish.packet)
		cloned.packet = &packet
	}
	return cloned
}

func delegateFinishMetadataEvents(events []delegatestore.Event, lease delegateLease, finish delegateFinish, outcome delegatestore.OutcomeStatus, reason string) []delegatestore.Event {
	if outcome != delegatestore.OutcomeExhausted {
		return events
	}
	for i := range events {
		if events[i].RunFinished == nil {
			continue
		}
		events[i].RunFinished.Outcome.ExhaustionBudget = finish.exhaustionBudget
		events[i].RunFinished.Outcome.ExhaustionLimit = finish.exhaustionLimit
		if finish.exhaustionResumable != nil {
			resumable := *finish.exhaustionResumable
			events[i].RunFinished.Outcome.Resumable = &resumable
		}
	}
	if finish.exhaustionResumable != nil && !*finish.exhaustionResumable {
		events = append(events, delegatestore.Event{
			Kind:       delegatestore.EventDelegateResumabilityClosed,
			DelegateID: lease.delegateID,
			ResumabilityClosed: &delegatestore.ResumabilityClosed{
				Reason: reason,
			},
		})
	}
	return events
}

func (c *delegateTreeController) generationFinishedPlansLocked(lease delegateLease, deliveryID string) (delegateMutationPlans, context.CancelFunc) {
	c.releaseSettlementClaimsForLeaseLocked(lease)
	if c.stop != nil {
		if _, tracked := c.stop.active[lease]; tracked {
			delete(c.stop.active, lease)
			c.signalStopProgressLocked()
		}
	}
	cancel := c.releaseGenerationLocked(lease)
	c.evidenceVersion++
	plan := c.capturedPlanLocked(lease.delegateID)
	plans := delegateMutationPlans{updates: []delegateUpdatePlan{plan}}
	if delivery := c.newHeadDeliveryPlanLocked(lease.delegateID, deliveryID); delivery != nil {
		plans.deliveries = append(plans.deliveries, *delivery)
	}
	plans.deliveries = append(plans.deliveries, c.replayDeliveriesForOwnerLocked(lease.delegateID)...)
	return plans, cancel
}

func delegateRunFinishedEvent(lease delegateLease, outcome delegatestore.OutcomeStatus, disposition delegatestore.RunDisposition, reason string, endedAt time.Time, deliveryID string, packet *delegatestore.TerminalPacket) delegatestore.Event {
	return delegatestore.Event{
		Kind:       delegatestore.EventDelegateRunFinished,
		DelegateID: lease.delegateID,
		RunFinished: &delegatestore.RunFinished{
			Generation: lease.generation,
			Outcome: delegatestore.Outcome{
				Status:  outcome,
				Reason:  reason,
				EndedAt: endedAt,
			},
			Disposition: disposition,
			DeliveryID:  deliveryID,
			Packet:      packet,
		},
	}
}

func delegateDeliveryID(delegateID string, generation uint64) string {
	return delegateID + "/delivery/" + strconv.FormatUint(generation, 10)
}

func delegateMissingTerminalPacket() delegatestore.TerminalPacket {
	return delegateTerminalErrorPacket("delegate completed without an accepted communicate result")
}

func delegateStoppedTerminalPacket() delegatestore.TerminalPacket {
	return delegateTerminalErrorPacket("stopped by parent")
}

func delegateIsMissingTerminalPacket(packet delegatestore.TerminalPacket) bool {
	want := delegateMissingTerminalPacket()
	return packet.Kind == want.Kind &&
		string(packet.Message) == string(want.Message) &&
		len(packet.StructuredResult) == 0 &&
		packet.StructuredResultValid == nil &&
		packet.StructuredResultReason == "" &&
		len(packet.Warnings) == 0 &&
		len(packet.Metadata) == 0
}

func delegateTerminalErrorPacket(reason string) delegatestore.TerminalPacket {
	message := reason
	if message == "" {
		message = "delegate generation ended before reporting a result"
	}
	if len(message) > delegateFinishReasonLimit {
		message = message[:delegateFinishReasonLimit]
	}
	raw, _ := json.Marshal(message)
	return delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: raw}
}

func delegatePacketDisposition(packet delegatestore.TerminalPacket) delegatestore.RunDisposition {
	if packet.Kind == delegatestore.PacketReported {
		return delegatestore.DispositionReported
	}
	return delegatestore.DispositionTerminalError
}

func delegatePreparedFinish(packet delegatestore.TerminalPacket) delegateFinish {
	finish := delegateFinish{outcome: delegatestore.OutcomeFailed, disposition: delegatestore.DispositionTerminalError, reason: "terminal_error"}
	if packet.Kind == delegatestore.PacketReported {
		finish = delegateFinish{outcome: delegatestore.OutcomeCompleted, disposition: delegatestore.DispositionReported}
	}
	if delegateIsMissingTerminalPacket(packet) {
		return delegateFinish{outcome: delegatestore.OutcomeFailed, disposition: delegatestore.DispositionTerminalError, reason: "missing_terminal"}
	}
	if len(packet.Metadata) == 0 {
		return finish
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
		return finish
	}
	if endedAt, err := time.Parse(time.RFC3339Nano, metadata.RunEndedAt); err == nil {
		finish.endedAt = endedAt
	}
	if packet.Kind == delegatestore.PacketReported {
		return finish
	}
	switch metadata.Outcome {
	case delegatestore.OutcomeCancelled:
		if metadata.Reason == "cancelled" {
			finish.outcome = delegatestore.OutcomeCancelled
			finish.reason = metadata.Reason
		}
		return finish
	case delegatestore.OutcomeFailed:
		reason := strings.TrimSpace(metadata.Reason)
		if reason != "" {
			if len(reason) > delegateFinishReasonLimit {
				reason = reason[:delegateFinishReasonLimit]
			}
			finish.reason = reason
		}
		return finish
	case delegatestore.OutcomeExhausted:
	default:
		return finish
	}
	if metadata.ExhaustionLimit <= 0 || metadata.ExhaustionResumable == nil {
		return finish
	}
	resumable := *metadata.ExhaustionResumable
	switch metadata.ExhaustionBudget {
	case delegatestore.ExhaustionBudgetToolRounds:
		if !resumable {
			return finish
		}
		finish.reason = "tool_round_budget_exhausted"
	case delegatestore.ExhaustionBudgetTurns:
		if resumable {
			return finish
		}
		finish.reason = "turn_budget_exhausted"
	default:
		return finish
	}
	finish.outcome = delegatestore.OutcomeExhausted
	finish.exhaustionBudget = metadata.ExhaustionBudget
	finish.exhaustionLimit = metadata.ExhaustionLimit
	finish.exhaustionResumable = &resumable
	return finish
}

func cloneDelegateTerminalPacket(packet delegatestore.TerminalPacket) delegatestore.TerminalPacket {
	clone := packet
	clone.Message = append(json.RawMessage(nil), packet.Message...)
	clone.StructuredResult = append(json.RawMessage(nil), packet.StructuredResult...)
	clone.Warnings = append([]string(nil), packet.Warnings...)
	clone.Metadata = append(json.RawMessage(nil), packet.Metadata...)
	if packet.StructuredResultValid != nil {
		valid := *packet.StructuredResultValid
		clone.StructuredResultValid = &valid
	}
	return clone
}
