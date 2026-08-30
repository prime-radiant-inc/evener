package agent

import (
	"context"
	"encoding/json"
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
	decision := c.reduceSupervisionBoundaryIntent(finishIntent{lease: lease, mode: mode})
	return decision.boundary, decision.err
}

// supervisionSuppressedLocked reports whether ordinary supervision must be suppressed
// (controller closing, generation stopping or stop-pending, or recovery required).
// The not-running/not-ready path (errDelegateTargetBusy) is not part of this predicate.
// Caller holds c.mu.
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
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := c.reduceBeginFinalizationIntent(finishIntent{lease: lease, mode: mode})
	return decision.claim, decision.continued, decision.err
}

// BeginRunFinalization binds the sampled run error to the exact settlement
// claim. Packetless no-action authority requires this fact to be known and nil;
// generic controller callers cannot obtain that authority through a claim whose
// run result was not supplied.
func (c *delegateTreeController) BeginRunFinalization(lease delegateLease, mode delegateSettlementMode, runErr error) (*delegateSettlementClaim, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := c.reduceBeginFinalizationIntent(finishIntent{lease: lease, mode: mode, runErrorKnown: true, runErr: runErr})
	return decision.claim, decision.continued, decision.err
}

func (c *delegateTreeController) CompleteSettlement(claim *delegateSettlementClaim, supplied *delegatestore.TerminalPacket) (delegateMutationPlans, error) {
	c.mu.Lock()
	plans, cancel, err := c.executeFinishDecisionLocked(c.reduceCompleteSettlementIntent(finishIntent{claim: claim, packet: supplied}))
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return plans, err
}

func (c *delegateTreeController) AttentionResolutionsForFinalization(claim *delegateSettlementClaim) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := c.reduceAttentionResolutionsIntent(finishIntent{claim: claim})
	if decision.err != nil {
		return delegateMutationPlans{}, decision.err
	}
	return delegateMutationPlans{
		attention:             decision.attentionPlans,
		attentionFinalization: claim,
	}, nil
}

// prepareNoAction binds the run's ordinary terminal fallback to the exact
// eligible attention claim before process-local terminal state is published.
// The claim stays live so only FinishNoAction can consume this authority.
func (c *delegateTreeController) prepareNoAction(claim *delegateSettlementClaim, fallback delegateFinish) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := c.reducePrepareNoActionIntent(finishIntent{claim: claim, finish: fallback})
	return decision.recorded, decision.err
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
	decision := c.reduceRequireFinalizationRecoveryIntent(finishIntent{claim: claim})
	return decision.err
}

// ReportFinalizationQuiesced releases only the process-local runner fence for
// the exact generation and resident runtime. Durable recovery authority remains
// latched until reconciliation closes or repairs that generation.
func (c *delegateTreeController) ReportFinalizationQuiesced(lease delegateLease, runtime *Session) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	decision := c.reduceReportQuiescedIntent(finishIntent{lease: lease, runtime: runtime, stalePolicy: finishStaleSwallow})
	return decision.err
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
	plans, cancel, err := c.executeFinishDecisionLocked(c.reduceGenerationFinishIntent(finishIntent{
		lease:       lease,
		finish:      finish,
		stalePolicy: finishStaleSuppress,
	}))
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
	noAction := c.reduceNoActionFinishIntent(finishIntent{claim: claim})
	if noAction.err != nil {
		c.mu.Unlock()
		return delegateMutationPlans{}, noAction.err
	}
	plans, cancel, err := c.executeFinishDecisionLocked(c.reduceGenerationFinishIntent(finishIntent{
		lease:              claim.lease,
		finish:             noAction.finish,
		authorizedNoAction: true,
	}))
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return plans, err
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
