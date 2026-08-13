package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

type delegateAttentionResolution string

const (
	delegateAttentionConsumed  delegateAttentionResolution = "consumed"
	delegateAttentionDiscarded delegateAttentionResolution = "discarded"
)

type shellNotificationIdentity struct {
	jobID              string
	terminalGeneration string
}

type shellRuntimeLossEvidence struct {
	runningJobIDs       []string
	pendingNotification []shellNotificationIdentity
}

type delegateAttentionCleanupPlan struct {
	requestSeq      uint64
	evidenceVersion uint64
	delegateID      string
	transcriptRef   string
	attentionID     string
	disposition     delegateAttentionResolution
	runtime         *Session
}

type delegateShellRepairPlan struct {
	delegateID          string
	storePath           string
	runningJobIDs       []string
	pendingNotification []shellNotificationIdentity
	suppressOwnerNotify bool
}

type delegateReconcileRequirements struct {
	evidenceVersion      uint64
	shellStores          map[string]string
	attentionTranscripts map[string]string
}

type delegateReconcileEvidence struct {
	evidenceVersion uint64
	shells          map[string]shellRuntimeLossEvidence
	attention       map[string][]string
}

func (c *delegateTreeController) ReconcileRequirements() delegateReconcileRequirements {
	c.mu.Lock()
	defer c.mu.Unlock()
	requirements := delegateReconcileRequirements{
		evidenceVersion:      c.evidenceVersion,
		shellStores:          make(map[string]string),
		attentionTranscripts: make(map[string]string),
	}
	for id, aggregate := range c.durable {
		if aggregate == nil {
			continue
		}
		requirements.shellStores[id] = filepath.Join(jobsDir(c.stateDir, aggregate.Descriptor.ChildSessionID), "jobs.jsonl")
		requirements.attentionTranscripts[id] = aggregate.Descriptor.TranscriptRef
	}
	return requirements
}

func (c *delegateTreeController) Reconcile(evidence delegateReconcileEvidence) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if evidence.evidenceVersion != c.evidenceVersion {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	if !delegateReconcileEvidenceMatchesState(evidence, c.durable) {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	plans, err := c.reconcileRuntimeLostFromEvidenceLocked()
	if err != nil {
		return delegateMutationPlans{}, err
	}
	if evidence.evidenceVersion != c.evidenceVersion {
		return plans, nil
	}
	allIDs := make([]string, 0, len(c.durable))
	for id := range c.durable {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)
	for _, id := range allIDs {
		shell := evidence.shells[id]
		covered := c.stopCoversLocked(id)
		if len(shell.runningJobIDs) == 0 && (!covered || len(shell.pendingNotification) == 0) {
			continue
		}
		plans.shellRepairs = append(plans.shellRepairs, delegateShellRepairPlan{
			delegateID:          id,
			storePath:           filepath.Join(jobsDir(c.stateDir, c.durable[id].Descriptor.ChildSessionID), "jobs.jsonl"),
			runningJobIDs:       append([]string(nil), shell.runningJobIDs...),
			pendingNotification: append([]shellNotificationIdentity(nil), shell.pendingNotification...),
			suppressOwnerNotify: covered,
		})
	}
	if c.stop == nil {
		return plans, nil
	}
	stop := c.stop
	if len(stop.active) != 0 || len(stop.starts) != 0 || len(stop.work) != 0 || len(stop.deliveries) != 0 {
		return plans, nil
	}
	ids := make([]string, 0, len(stop.members))
	for id := range stop.members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		attention := append([]string(nil), evidence.attention[id]...)
		sort.Strings(attention)
		for _, attentionID := range attention {
			if attentionID == "" {
				continue
			}
			var runtime *Session
			if live := c.live[id]; live != nil {
				runtime = live.runtime
			}
			plans.attention = append(plans.attention, delegateAttentionCleanupPlan{
				requestSeq:      stop.requestSeq,
				evidenceVersion: evidence.evidenceVersion,
				delegateID:      id,
				transcriptRef:   c.durable[id].Descriptor.TranscriptRef,
				attentionID:     attentionID,
				disposition:     delegateAttentionDiscarded,
				runtime:         runtime,
			})
			return plans, nil
		}
	}
	if len(plans.shellRepairs) != 0 || len(plans.attention) != 0 {
		return plans, nil
	}
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateSubtreeStopCompleted,
		DelegateID: stop.targetID,
		SubtreeStopCompleted: &delegatestore.SubtreeStopCompleted{
			RequestSeq: stop.requestSeq,
		},
	}); err != nil {
		return delegateMutationPlans{}, err
	}
	close(stop.done)
	c.stop = nil
	c.evidenceVersion++
	for _, id := range ids {
		plans.updates = append(plans.updates, c.capturedPlanLocked(id))
	}
	allIDs = make([]string, 0, len(c.durable))
	for id := range c.durable {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)
	for _, id := range allIDs {
		aggregate := c.durable[id]
		if aggregate == nil || len(aggregate.PendingDeliveries) == 0 {
			continue
		}
		if plan := c.newHeadDeliveryPlanLocked(id, aggregate.PendingDeliveries[0].DeliveryID); plan != nil {
			plans.deliveries = append(plans.deliveries, *plan)
		}
	}
	return plans, nil
}

func delegateReconcileEvidenceMatchesState(evidence delegateReconcileEvidence, state delegatestore.State) bool {
	if len(evidence.shells) != len(state) || len(evidence.attention) != len(state) {
		return false
	}
	for id, aggregate := range state {
		if aggregate == nil {
			return false
		}
		if _, exists := evidence.shells[id]; !exists {
			return false
		}
		if _, exists := evidence.attention[id]; !exists {
			return false
		}
	}
	return true
}

func (c *delegateTreeController) ReportAttentionResolved(requestSeq, evidenceVersion uint64, delegateID, attentionID string, disposition delegateAttentionResolution, runtime *Session) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop == nil || c.stop.requestSeq != requestSeq || c.evidenceVersion != evidenceVersion || attentionID == "" || disposition != delegateAttentionDiscarded {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	if _, covered := c.stop.members[delegateID]; !covered {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	var currentRuntime *Session
	if live := c.live[delegateID]; live != nil {
		currentRuntime = live.runtime
	}
	if currentRuntime != runtime {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	c.evidenceVersion++
	return delegateMutationPlans{}, nil
}

func (c *delegateTreeController) executeDelegateAttentionCleanup(plan delegateAttentionCleanupPlan) error {
	path, expectedSessionID, err := delegateTranscriptPathFromRef(c.stateDir, plan.transcriptRef)
	if err != nil {
		return err
	}
	if err := appendColdAttentionResolution(path, expectedSessionID, []string{plan.attentionID}, plan.disposition); err != nil {
		return err
	}
	_, err = c.ReportAttentionResolved(plan.requestSeq, plan.evidenceVersion, plan.delegateID, plan.attentionID, plan.disposition, plan.runtime)
	return err
}

func (c *delegateTreeController) reconcileRuntimeLostFromEvidenceLocked() (delegateMutationPlans, error) {
	plans := delegateMutationPlans{}
	remaining := c.reconcileOrder[:0]
	for _, lease := range c.reconcileOrder {
		aggregate := c.durable[lease.delegateID]
		if aggregate == nil || !aggregate.CurrentRunOpen || aggregate.Generation != lease.generation {
			continue
		}
		if live := c.live[lease.delegateID]; live != nil && live.binding != nil && live.binding.lease == lease {
			remaining = append(remaining, lease)
			continue
		}
		endedAt := c.now()
		var events []delegatestore.Event
		switch aggregate.Phase {
		case delegatestore.PhaseRunning:
			packet := delegatestore.TerminalPacket{
				Kind:    delegatestore.PacketTerminalError,
				Message: json.RawMessage(`"delegate runtime was lost before the generation settled"`),
			}
			terminal, finish := terminalFinishBatch(lease, delegatestore.OutcomeFailed, "runtime_lost", endedAt, packet)
			events = []delegatestore.Event{terminal, finish}
		case delegatestore.PhaseSettling:
			if aggregate.PreparedTerminal == nil {
				return delegateMutationPlans{}, fmt.Errorf("reconcile delegate %s: settling without prepared terminal", lease.delegateID)
			}
			outcome, disposition, reason := delegatePreparedFinish(*aggregate.PreparedTerminal)
			events = []delegatestore.Event{delegateRunFinishedEvent(
				lease,
				outcome,
				disposition,
				reason,
				endedAt,
				delegateDeliveryID(lease.delegateID, lease.generation),
				nil,
			)}
		case delegatestore.PhaseStopping:
			stoppedPacket := delegateStoppedTerminalPacket()
			events = []delegatestore.Event{delegateRunFinishedEvent(
				lease,
				delegatestore.OutcomeStopped,
				delegatestore.DispositionTerminalError,
				"stopped_by_parent",
				endedAt,
				delegateDeliveryID(lease.delegateID, lease.generation),
				&stoppedPacket,
			)}
		default:
			return delegateMutationPlans{}, fmt.Errorf("reconcile delegate %s: open run has phase %s", lease.delegateID, aggregate.Phase)
		}
		if _, err := c.appendLocked(events...); err != nil {
			return delegateMutationPlans{}, fmt.Errorf("reconcile delegate %s runtime loss: %w", lease.delegateID, err)
		}
		c.evidenceVersion++
		plans.updates = append(plans.updates, c.capturedPlanLocked(lease.delegateID))
		if delivery := c.newHeadDeliveryPlanLocked(lease.delegateID, delegateDeliveryID(lease.delegateID, lease.generation)); delivery != nil {
			plans.deliveries = append(plans.deliveries, *delivery)
		}
	}
	c.reconcileOrder = remaining
	return plans, nil
}

func delegateOpenRunOrder(events []delegatestore.Event, state delegatestore.State) []delegateLease {
	order := make([]delegateLease, 0)
	for _, event := range events {
		if event.RunStarted == nil {
			continue
		}
		aggregate := state[event.DelegateID]
		if aggregate == nil || !aggregate.CurrentRunOpen || aggregate.Generation != event.RunStarted.Generation {
			continue
		}
		order = append(order, delegateLease{delegateID: event.DelegateID, generation: event.RunStarted.Generation})
	}
	return order
}

func (c *delegateTreeController) restorePendingStop(events []delegatestore.Event) error {
	var requestSeq uint64
	for _, aggregate := range c.durable {
		if aggregate == nil || aggregate.PendingStopSeq == 0 {
			continue
		}
		if requestSeq != 0 && requestSeq != aggregate.PendingStopSeq {
			return fmt.Errorf("restore delegate stops %d and %d", requestSeq, aggregate.PendingStopSeq)
		}
		requestSeq = aggregate.PendingStopSeq
	}
	if requestSeq == 0 {
		return nil
	}
	targetID := ""
	for _, event := range events {
		if event.Seq == requestSeq && event.Kind == delegatestore.EventDelegateSubtreeStopRequested && event.SubtreeStopRequested != nil {
			targetID = event.SubtreeStopRequested.TargetDelegateID
			break
		}
	}
	if targetID == "" {
		return fmt.Errorf("restore delegate stop %d without request event", requestSeq)
	}
	members := make(map[string]struct{})
	for id, aggregate := range c.durable {
		if aggregate != nil && aggregate.PendingStopSeq == requestSeq {
			members[id] = struct{}{}
		}
	}
	c.stop = &delegateStopState{
		requestSeq: requestSeq,
		targetID:   targetID,
		members:    members,
		active:     make(map[delegateLease]struct{}),
		starts:     make(map[uint64]struct{}),
		work:       make(map[delegateWorkToken]string),
		deliveries: make(map[delegateDeliveryToken]struct{}),
		done:       make(chan struct{}),
	}
	return nil
}
