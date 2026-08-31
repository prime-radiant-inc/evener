package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

type delegateAttentionResolution string

const (
	delegateAttentionConsumed  delegateAttentionResolution = schema.AttentionDispositionConsumed
	delegateAttentionDiscarded delegateAttentionResolution = schema.AttentionDispositionDiscarded
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
	lease           delegateLease
	delegateID      string
	transcriptRef   string
	attentionID     string
	disposition     delegateAttentionResolution
	runtime         *Session
	stabilize       bool
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

type delegateAttentionTransferPlan struct {
	sourceDelegateID string
	sourceRef        string
	targetDelegateID string
	targetRef        string
}

type delegateOwedAttentionStart struct {
	delegateID  string
	parentID    string
	attentionID string
	generation  uint64
	pendingIDs  []string
}

func (c *delegateTreeController) takeOwedAttentionAdmission() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.owedAdmission {
		return false
	}
	c.owedAdmission = false
	return true
}

// owedAttentionStartsFromTranscripts finds accepted attention generations that
// survived in a child transcript but not in the delegate journal. A zero
// generation is historical resolution metadata and never creates work.
func (c *delegateTreeController) owedAttentionStartsFromTranscripts() ([]delegateOwedAttentionStart, error) {
	if c == nil {
		return nil, errors.New("delegate controller is nil")
	}
	c.mu.Lock()
	refs := make([]coldDelegateAttentionRef, 0, len(c.durable))
	generations := make(map[string]uint64, len(c.durable))
	parents := make(map[string]string, len(c.durable))
	runStarts := make(map[delegateLease]delegatestore.RunTrigger, len(c.runStarts))
	maps.Copy(runStarts, c.runStarts)
	stateDir := c.stateDir
	for id, aggregate := range c.durable {
		if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !delegateAttentionProjectionEligible(c.durable, id) {
			continue
		}
		refs = append(refs, coldDelegateAttentionRef{delegateID: id, transcriptRef: aggregate.Descriptor.TranscriptRef})
		generations[id] = aggregate.Generation
		parents[id] = aggregate.Descriptor.ParentDelegateID
	}
	c.mu.Unlock()
	sort.Slice(refs, func(i, j int) bool { return refs[i].delegateID < refs[j].delegateID })

	owed := make([]delegateOwedAttentionStart, 0)
	for _, ref := range refs {
		path, sessionID, err := delegateTranscriptPathFromRef(stateDir, ref.transcriptRef)
		if err != nil {
			return nil, err
		}
		fold, err := readExistingDelegateAttentionFold(path, sessionID)
		if err != nil {
			return nil, fmt.Errorf("delegate %s attention transcript: %w", ref.delegateID, err)
		}
		resumes := make([]struct {
			attentionID string
			generation  uint64
		}, 0, len(fold.resumeGenerations))
		for attentionID, generation := range fold.resumeGenerations {
			if generation != 0 {
				resumes = append(resumes, struct {
					attentionID string
					generation  uint64
				}{attentionID: attentionID, generation: generation})
			}
		}
		sort.Slice(resumes, func(i, j int) bool {
			if resumes[i].generation != resumes[j].generation {
				return resumes[i].generation < resumes[j].generation
			}
			return resumes[i].attentionID < resumes[j].attentionID
		})
		journalGeneration := generations[ref.delegateID]
		for _, resume := range resumes {
			lease := delegateLease{delegateID: ref.delegateID, generation: resume.generation}
			if trigger, exists := runStarts[lease]; exists {
				if trigger != delegatestore.TriggerAttention {
					return nil, fmt.Errorf("delegate %s attention %q resume generation %d matches run trigger %q, want %q", ref.delegateID, resume.attentionID, resume.generation, trigger, delegatestore.TriggerAttention)
				}
				continue
			}
			if resume.generation <= journalGeneration {
				return nil, fmt.Errorf("delegate %s attention %q resume generation %d has no exact RunStarted at journal generation %d", ref.delegateID, resume.attentionID, resume.generation, journalGeneration)
			}
			if resume.generation != journalGeneration+1 {
				return nil, fmt.Errorf("delegate %s attention %q owes generation %d after journal generation %d", ref.delegateID, resume.attentionID, resume.generation, journalGeneration)
			}
			if len(owed) != 0 && owed[len(owed)-1].delegateID == ref.delegateID {
				return nil, fmt.Errorf("delegate %s owes multiple attention generations", ref.delegateID)
			}
			owed = append(owed, delegateOwedAttentionStart{
				delegateID:  ref.delegateID,
				parentID:    parents[ref.delegateID],
				attentionID: resume.attentionID,
				generation:  resume.generation,
				pendingIDs:  fold.pendingIDs(),
			})
		}
	}
	return owed, nil
}

func delegateRunStartIndex(events []delegatestore.Event) map[delegateLease]delegatestore.RunTrigger {
	index := make(map[delegateLease]delegatestore.RunTrigger)
	for _, event := range events {
		if event.RunStarted != nil {
			index[delegateLease{delegateID: event.DelegateID, generation: event.RunStarted.Generation}] = event.RunStarted.Trigger
		}
	}
	return index
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
	var cancel context.CancelFunc
	defer func() {
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}()
	if evidence.evidenceVersion != c.evidenceVersion {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	if !delegateReconcileEvidenceMatchesState(evidence, c.durable) {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	plans, generationCancel, err := c.reconcileRecoveryRequiredStopLocked()
	if err != nil {
		return delegateMutationPlans{}, err
	}
	if generationCancel != nil {
		cancel = generationCancel
	}
	if len(plans.updates) != 0 || len(plans.attention) != 0 {
		return plans, nil
	}
	plans, err = c.reconcileRuntimeLostFromEvidenceLocked()
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
		shell, known := c.repairableShellEvidenceLocked(id, evidence.shells[id])
		if !known {
			continue
		}
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
	if len(stop.active) != 0 || len(stop.starts) != 0 || len(stop.work) != 0 || len(stop.deliveries) != 0 || len(stop.quietClaims) != 0 || len(stop.steeringClaims) != 0 || len(stop.modelClaims) != 0 || len(stop.settlementClaims) != 0 || len(stop.watchEnqueues) != 0 || len(stop.watchDeliveries) != 0 {
		return plans, nil
	}
	ids := make([]string, 0, len(stop.members))
	for id := range stop.members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		attention := append([]string(nil), evidence.attention[id]...)
		// A wake noted after the evidence snapshot was taken is bound to this
		// stop member exactly the same way: its transcript open is already
		// durable (the arm paths append before they note), so the same discard
		// machinery resolves it. The controller mutex is held across this
		// check and the completion append below, so a wake cannot slip into
		// that window twice and leak past the completed stop.
		for attentionID := range c.attentionWakeIDs[id] {
			if !slices.Contains(attention, attentionID) {
				attention = append(attention, attentionID)
			}
		}
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

func (c *delegateTreeController) reconcileRecoveryRequiredStopLocked() (delegateMutationPlans, context.CancelFunc, error) {
	if c.stop == nil {
		return delegateMutationPlans{}, nil, nil
	}
	stop := c.stop
	if len(stop.starts) != 0 || len(stop.work) != 0 || len(stop.deliveries) != 0 || len(stop.quietClaims) != 0 || len(stop.steeringClaims) != 0 || len(stop.modelClaims) != 0 || len(stop.watchEnqueues) != 0 || len(stop.watchDeliveries) != 0 {
		return delegateMutationPlans{}, nil, nil
	}
	leases := make([]delegateLease, 0, len(stop.active))
	for lease := range stop.active {
		leases = append(leases, lease)
	}
	sort.Slice(leases, func(i, j int) bool {
		if leases[i].delegateID != leases[j].delegateID {
			return leases[i].delegateID < leases[j].delegateID
		}
		return leases[i].generation < leases[j].generation
	})
	for _, lease := range leases {
		if _, covered := stop.members[lease.delegateID]; !covered {
			continue
		}
		aggregate := c.durable[lease.delegateID]
		live := c.live[lease.delegateID]
		if aggregate == nil || aggregate.Generation != lease.generation || !aggregate.CurrentRunOpen || aggregate.Phase != delegatestore.PhaseStopping || aggregate.PendingStopSeq != stop.requestSeq || live == nil || live.binding == nil || live.binding.lease != lease || !live.recoveryRequired {
			continue
		}
		if live.recoveryRunnerPending {
			continue
		}
		if len(live.attentionIDs) != 0 {
			return delegateMutationPlans{attention: []delegateAttentionCleanupPlan{{
				lease:         lease,
				delegateID:    lease.delegateID,
				transcriptRef: aggregate.Descriptor.TranscriptRef,
				attentionID:   live.attentionIDs[0],
				runtime:       live.runtime,
				stabilize:     true,
			}}}, nil, nil
		}
		packet := delegateStoppedTerminalPacket()
		deliveryID := delegateDeliveryID(lease.delegateID, lease.generation)
		if _, err := c.appendLocked(delegateRunFinishedEvent(
			lease,
			delegatestore.OutcomeStopped,
			delegatestore.DispositionTerminalError,
			"stopped_by_parent",
			c.now(),
			deliveryID,
			&packet,
		)); err != nil {
			return delegateMutationPlans{}, nil, fmt.Errorf("reconcile delegate %s recovery-required stop: %w", lease.delegateID, err)
		}
		plans, cancel := c.generationFinishedPlansLocked(lease, deliveryID)
		return plans, cancel, nil
	}
	return delegateMutationPlans{}, nil, nil
}

func (c *delegateTreeController) closeMissingRestoreInputs(reasons map[string]string) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(reasons))
	for id := range reasons {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	plans := delegateMutationPlans{}
	for _, id := range ids {
		reason := reasons[id]
		aggregate := c.durable[id]
		if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || strings.TrimSpace(reason) == "" {
			continue
		}
		plan, err := c.appendResumabilityClosureLocked(id, delegatestore.Event{
			Kind:               delegatestore.EventDelegateResumabilityClosed,
			DelegateID:         id,
			ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: reason},
		})
		if err != nil {
			return delegateMutationPlans{}, err
		}
		c.evidenceVersion++
		plans.updates = append(plans.updates, plan)
	}
	return plans, nil
}

// repairPermanentlyUnreachableDelegateAttention transfers pending model-bound
// input only after resumability has closed monotonically. Transcript I/O is
// performed from an immutable tree snapshot without the controller mutex. It
// also transfers the attention of live descendants that a permanently closed
// ancestor fences off to the root session -- each message under its own
// original identity, so nothing is lost silently and no wake retries forever.
func repairPermanentlyUnreachableDelegateAttention(c *delegateTreeController) error {
	if c == nil {
		return errors.New("delegate controller is nil")
	}
	c.mu.Lock()
	stateDir := c.stateDir
	rootSessionID := c.rootSessionID
	open := c.attentionOpen
	now := c.now
	ids := make([]string, 0, len(c.durable))
	for id, aggregate := range c.durable {
		if aggregate != nil && !aggregate.Resumable && aggregate.Phase == delegatestore.PhaseClosed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	plans := make([]delegateAttentionTransferPlan, 0, len(ids))
	for _, id := range ids {
		aggregate := c.durable[id]
		targetID := nearestReachableAttentionAncestorLocked(c.durable, aggregate.Descriptor.ParentDelegateID)
		targetRef := ""
		if targetID != "" {
			targetRef = c.durable[targetID].Descriptor.TranscriptRef
		}
		plans = append(plans, delegateAttentionTransferPlan{
			sourceDelegateID: id,
			sourceRef:        aggregate.Descriptor.TranscriptRef,
			targetDelegateID: targetID,
			targetRef:        targetRef,
		})
	}
	fencedIDs := make([]string, 0, len(c.durable))
	for id, aggregate := range c.durable {
		if aggregate == nil || !aggregate.Resumable || aggregate.Phase != delegatestore.PhaseIdle || aggregate.PendingStopSeq != 0 {
			continue
		}
		blocked, closedAncestorID := c.ancestorFenceLocked(aggregate.Descriptor.ParentDelegateID)
		if !blocked || closedAncestorID == "" {
			continue
		}
		fencedIDs = append(fencedIDs, id)
	}
	sort.Strings(fencedIDs)
	for _, id := range fencedIDs {
		// A live descendant fenced off permanently transfers straight to the
		// root: an empty target is the existing "escalate to root" shape below.
		plans = append(plans, delegateAttentionTransferPlan{
			sourceDelegateID: id,
			sourceRef:        c.durable[id].Descriptor.TranscriptRef,
		})
	}
	c.mu.Unlock()
	if open == nil {
		open = transcript.OpenWriterForSession
	}
	for _, plan := range plans {
		sourcePath, sourceSessionID, err := delegateTranscriptPathFromRef(stateDir, plan.sourceRef)
		if err != nil {
			return fmt.Errorf("delegate %s source transcript: %w", plan.sourceDelegateID, err)
		}
		targetPath := transcriptPath(stateDir, rootSessionID)
		targetSessionID := rootSessionID
		if plan.targetDelegateID != "" {
			targetPath, targetSessionID, err = delegateTranscriptPathFromRef(stateDir, plan.targetRef)
			if err != nil {
				return fmt.Errorf("delegate %s ancestor %s transcript: %w", plan.sourceDelegateID, plan.targetDelegateID, err)
			}
		}
		fold, err := readDelegateAttentionFold(sourcePath, sourceSessionID)
		if err != nil {
			return fmt.Errorf("delegate %s attention fold: %w", plan.sourceDelegateID, err)
		}
		for _, attentionID := range fold.pendingIDs() {
			message := fold.content[attentionID]
			if _, err := appendColdDelegateAttentionMessageDurablyWithOpen(targetPath, targetSessionID, attentionID, message, now(), open); err != nil {
				return fmt.Errorf("delegate %s transfer attention %s: %w", plan.sourceDelegateID, attentionID, err)
			}
			if err := appendColdAttentionResolutionWithOpen(sourcePath, sourceSessionID, []string{attentionID}, delegateAttentionDiscarded, open); err != nil {
				return fmt.Errorf("delegate %s discard transferred attention %s: %w", plan.sourceDelegateID, attentionID, err)
			}
		}
	}
	return nil
}

func nearestReachableAttentionAncestorLocked(state delegatestore.State, parentID string) string {
	for parentID != "" {
		parent := state[parentID]
		if parent == nil {
			return ""
		}
		if parent.Resumable && parent.Phase == delegatestore.PhaseIdle && parent.PendingStopSeq == 0 {
			return parentID
		}
		parentID = parent.Descriptor.ParentDelegateID
	}
	return ""
}

// repairableShellEvidenceLocked removes process work that still has an exact
// live receipt. An uncommitted receipt defers the delegate because the durable
// job identity is not known yet and therefore cannot be excluded safely.
func (c *delegateTreeController) repairableShellEvidenceLocked(delegateID string, evidence shellRuntimeLossEvidence) (shellRuntimeLossEvidence, bool) {
	liveJobIDs := make(map[string]struct{})
	for _, work := range c.work {
		if work == nil || work.owner.delegateID != delegateID {
			continue
		}
		if !work.committed {
			return shellRuntimeLossEvidence{}, false
		}
		liveJobIDs[work.jobID] = struct{}{}
	}
	if len(liveJobIDs) == 0 {
		return evidence, true
	}
	filtered := shellRuntimeLossEvidence{}
	for _, jobID := range evidence.runningJobIDs {
		if _, live := liveJobIDs[jobID]; !live {
			filtered.runningJobIDs = append(filtered.runningJobIDs, jobID)
		}
	}
	for _, notification := range evidence.pendingNotification {
		if _, live := liveJobIDs[notification.jobID]; !live {
			filtered.pendingNotification = append(filtered.pendingNotification, notification)
		}
	}
	return filtered, true
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
	c.forgetDelegateAttentionLocked(delegateID, attentionID)
	c.evidenceVersion++
	return delegateMutationPlans{}, nil
}

func (c *delegateTreeController) ReportAttentionConsumed(lease delegateLease, attentionID string, runtime *Session) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil || attentionID == "" || aggregate.Trigger != delegatestore.TriggerAttention || live.runtime != runtime {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	index := -1
	for i, id := range live.attentionIDs {
		if id == attentionID {
			index = i
			break
		}
	}
	if index < 0 {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	live.attentionIDs = append(live.attentionIDs[:index], live.attentionIDs[index+1:]...)
	c.forgetDelegateAttentionLocked(lease.delegateID, attentionID)
	c.evidenceVersion++
	return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(lease.delegateID)}}, nil
}

func (c *delegateTreeController) ReportAttentionStabilized(lease delegateLease, attentionID string, runtime *Session) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil || attentionID == "" || aggregate.Phase != delegatestore.PhaseStopping || !live.recoveryRequired || live.runtime != runtime {
		return errDelegateStaleLease
	}
	index := -1
	for i, id := range live.attentionIDs {
		if id == attentionID {
			index = i
			break
		}
	}
	if index < 0 {
		return errDelegateStaleLease
	}
	live.attentionIDs = append(live.attentionIDs[:index], live.attentionIDs[index+1:]...)
	c.evidenceVersion++
	c.signalStopProgressLocked()
	return nil
}

func (c *delegateTreeController) executeDelegateAttentionCleanup(plan delegateAttentionCleanupPlan) error {
	if plan.stabilize {
		if plan.runtime == nil {
			return errDelegateDeliveryReceiverUnavailable
		}
		if err := plan.runtime.stabilizeAttentionForStop(plan.attentionID); err != nil {
			return err
		}
		return c.ReportAttentionStabilized(plan.lease, plan.attentionID, plan.runtime)
	}
	if err := c.resolveDelegateAttentionDurably(plan.runtime, plan.transcriptRef, []string{plan.attentionID}, plan.disposition); err != nil {
		return err
	}
	if plan.disposition == delegateAttentionConsumed {
		_, err := c.ReportAttentionConsumed(plan.lease, plan.attentionID, plan.runtime)
		return err
	}
	_, err := c.ReportAttentionResolved(plan.requestSeq, plan.evidenceVersion, plan.delegateID, plan.attentionID, plan.disposition, plan.runtime)
	return err
}

// resolveDelegateAttentionDurably appends attention resolution markers through
// the resident runtime's one writer when it exists, and through a cold writer
// on the receiver transcript otherwise.
func (c *delegateTreeController) resolveDelegateAttentionDurably(runtime *Session, transcriptRef string, ids []string, disposition delegateAttentionResolution) error {
	if runtime != nil {
		return runtime.resolveAttentionDurably(ids, disposition)
	}
	path, expectedSessionID, err := delegateTranscriptPathFromRef(c.stateDir, transcriptRef)
	if err != nil {
		return err
	}
	open := c.attentionOpen
	if open == nil {
		open = transcript.OpenWriterForSession
	}
	return appendColdAttentionResolutionWithOpen(path, expectedSessionID, ids, disposition, open)
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
			finish := delegatePreparedFinish(*aggregate.PreparedTerminal)
			if !finish.endedAt.IsZero() {
				endedAt = finish.endedAt
			}
			events = []delegatestore.Event{delegateRunFinishedEvent(
				lease,
				finish.outcome,
				finish.disposition,
				finish.reason,
				endedAt,
				delegateDeliveryID(lease.delegateID, lease.generation),
				nil,
			)}
			events = delegateFinishMetadataEvents(events, lease, finish, finish.outcome, finish.reason)
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
		plans.deliveries = append(plans.deliveries, c.replayDeliveriesForOwnerLocked(lease.delegateID)...)
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
	requestIndex := -1
	for i, event := range events {
		if event.Seq == requestSeq && event.Kind == delegatestore.EventDelegateSubtreeStopRequested && event.SubtreeStopRequested != nil {
			targetID = event.SubtreeStopRequested.TargetDelegateID
			requestIndex = i
			break
		}
	}
	if targetID == "" {
		return fmt.Errorf("restore delegate stop %d without request event", requestSeq)
	}
	stateAtAdmission, err := delegatestore.Fold(events[:requestIndex])
	if err != nil {
		return fmt.Errorf("restore delegate stop %d admission: %w", requestSeq, err)
	}
	members := make(map[string]struct{})
	for id, aggregate := range c.durable {
		if aggregate != nil && aggregate.PendingStopSeq == requestSeq {
			members[id] = struct{}{}
		}
	}
	previousLifecycle, outcome := classifyDelegateStopAdmission(stateAtAdmission, targetID, members)
	c.stop = &delegateStopState{
		requestSeq:        requestSeq,
		targetID:          targetID,
		previousLifecycle: previousLifecycle,
		outcome:           outcome,
		members:           members,
		active:            make(map[delegateLease]struct{}),
		starts:            make(map[uint64]struct{}),
		work:              make(map[delegateWorkToken]string),
		deliveries:        make(map[delegateDeliveryToken]struct{}),
		quietClaims:       make(map[uint64]struct{}),
		steeringClaims:    make(map[uint64]struct{}),
		modelClaims:       make(map[uint64]struct{}),
		settlementClaims:  make(map[uint64]struct{}),
		watchEnqueues:     make(map[uint64]struct{}),
		watchDeliveries:   make(map[uint64]struct{}),
		done:              make(chan struct{}),
		progress:          make(chan struct{}, 1),
	}
	return nil
}
