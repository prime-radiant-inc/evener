package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
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
	token uint64
	lease delegateLease
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
	if c.closing || aggregate.Phase == delegatestore.PhaseStopping || aggregate.PendingStopSeq != 0 || live.recoveryRequired {
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

// BeginSettlement makes the final pending-steer decision and fences new work
// while the runtime performs its last cleanup and samples terminal evidence.
func (c *delegateTreeController) BeginSettlement(lease delegateLease) (*delegateSettlementClaim, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		return nil, false, err
	}
	if len(live.pendingSteers) != 0 || c.hasSteeringClaimLocked(lease) {
		return nil, true, nil
	}
	if c.hasSettlementClaimLocked(lease) {
		return nil, false, errDelegateTargetBusy
	}
	c.nextToken++
	claim := &delegateSettlementClaim{token: c.nextToken, lease: lease}
	c.settlementClaims[claim.token] = claim
	c.evidenceVersion++
	return claim, false, nil
}

func (c *delegateTreeController) CompleteSettlement(claim *delegateSettlementClaim, supplied *delegatestore.TerminalPacket) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.settlementClaims[claim.token] != claim {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	delete(c.settlementClaims, claim.token)
	if _, _, err := c.admitLeaseLocked(claim.lease, delegatestore.PhaseRunning); err != nil {
		c.evidenceVersion++
		return delegateMutationPlans{}, err
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
		c.evidenceVersion++
		return delegateMutationPlans{}, err
	}
	c.evidenceVersion++
	plan := c.capturedPlanLocked(claim.lease.delegateID)
	return delegateMutationPlans{updates: []delegateUpdatePlan{plan}}, nil
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
	var cancel context.CancelFunc
	defer func() {
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}()
	aggregate, _, err := c.exactLeaseLocked(lease)
	if err != nil {
		if errors.Is(err, errDelegateStaleLease) {
			return delegateMutationPlans{}, nil
		}
		return delegateMutationPlans{}, err
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
			return delegateMutationPlans{}, fmt.Errorf("delegate %q settling without prepared terminal", lease.delegateID)
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
		events = []delegatestore.Event{delegateRunFinishedEvent(lease, outcome, disposition, reason, endedAt, deliveryID, nil)}

	case delegatestore.PhaseStopping:
		packet := delegateStoppedTerminalPacket()
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
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	events = delegateFinishMetadataEvents(events, lease, finish, outcome, reason)

	if _, err := c.appendLocked(events...); err != nil {
		return delegateMutationPlans{}, err
	}
	plans, generationCancel := c.generationFinishedPlansLocked(lease, deliveryID)
	cancel = generationCancel
	return plans, nil
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
