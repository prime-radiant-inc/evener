package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

const delegateFinishReasonLimit = 512

func (c *delegateTreeController) BeginSettlement(lease delegateLease, supplied *delegatestore.TerminalPacket) (bool, delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		return false, delegateMutationPlans{}, err
	}
	if len(live.pendingSteers) != 0 {
		return true, delegateMutationPlans{}, nil
	}
	packet := delegateMissingTerminalPacket()
	if supplied != nil {
		packet = cloneDelegateTerminalPacket(*supplied)
	}
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateTerminalPrepared,
		DelegateID: lease.delegateID,
		TerminalPrepared: &delegatestore.TerminalPrepared{
			Generation: lease.generation,
			Packet:     packet,
		},
	}); err != nil {
		return false, delegateMutationPlans{}, err
	}
	plan := c.capturedPlanLocked(lease.delegateID)
	return false, delegateMutationPlans{updates: []delegateUpdatePlan{plan}}, nil
}

func (c *delegateTreeController) FinishGeneration(lease delegateLease, finish delegateFinish) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
		outcome, disposition, reason = delegatePreparedFinish(*aggregate.PreparedTerminal)
		deliveryID = delegateDeliveryID(lease.delegateID, lease.generation)
		events = []delegatestore.Event{delegateRunFinishedEvent(lease, outcome, disposition, reason, endedAt, deliveryID, nil)}

	case delegatestore.PhaseStopping:
		packet := delegateTerminalErrorPacket("stopped by parent")
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

	if _, err := c.appendLocked(events...); err != nil {
		return delegateMutationPlans{}, err
	}
	return c.generationFinishedPlansLocked(lease, deliveryID), nil
}

func (c *delegateTreeController) generationFinishedPlansLocked(lease delegateLease, deliveryID string) delegateMutationPlans {
	c.releaseGenerationLocked(lease)
	plan := c.capturedPlanLocked(lease.delegateID)
	plans := delegateMutationPlans{updates: []delegateUpdatePlan{plan}}
	if delivery := c.newHeadDeliveryPlanLocked(lease.delegateID, deliveryID); delivery != nil {
		plans.deliveries = append(plans.deliveries, *delivery)
	}
	return plans
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

func delegatePreparedFinish(packet delegatestore.TerminalPacket) (delegatestore.OutcomeStatus, delegatestore.RunDisposition, string) {
	if packet.Kind == delegatestore.PacketReported {
		return delegatestore.OutcomeCompleted, delegatestore.DispositionReported, ""
	}
	if delegateIsMissingTerminalPacket(packet) {
		return delegatestore.OutcomeFailed, delegatestore.DispositionTerminalError, "missing_terminal"
	}
	return delegatestore.OutcomeFailed, delegatestore.DispositionTerminalError, "terminal_error"
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
