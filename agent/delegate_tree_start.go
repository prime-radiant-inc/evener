package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/identifier"
)

type delegateCapacityKind uint8

const (
	delegateTurnCapacity delegateCapacityKind = iota
	delegateDriveCapacity
)

type delegateStartReservation struct {
	delegateID     string
	descriptor     delegatestore.Descriptor
	transcriptPath string
	worktreePath   string
}

type delegateStartRecord struct {
	receipt        *delegateStartReservation
	token          uint64
	delegateID     string
	generation     uint64
	trigger        delegatestore.RunTrigger
	capacityKind   delegateCapacityKind
	ctx            context.Context
	cancel         context.CancelFunc
	create         bool
	descriptor     delegatestore.Descriptor
	transcriptPath string
	worktreePath   string
	runtime        *Session
	attentionID    string
	waiter         *delegateInlineWaiter
}

type delegateStartCommit struct {
	lease          delegateLease
	plan           delegateUpdatePlan
	ctx            context.Context
	descriptor     delegatestore.Descriptor
	transcriptPath string
	worktreePath   string
}

type delegateFinish struct {
	outcome     delegatestore.OutcomeStatus
	disposition delegatestore.RunDisposition
	reason      string
	packet      *delegatestore.TerminalPacket
	endedAt     time.Time
}

type delegateCommittedStartFailureDisposition uint8

const (
	delegateCommittedStartFailureStopWon delegateCommittedStartFailureDisposition = iota + 1
	delegateCommittedStartFailureAppendFailed
)

type delegateCommittedStartFailureError struct {
	disposition delegateCommittedStartFailureDisposition
	cause       error
}

func (e *delegateCommittedStartFailureError) Error() string {
	return e.cause.Error()
}

func (e *delegateCommittedStartFailureError) Unwrap() error {
	return e.cause
}

func committedStartFailureDisposition(err error) delegateCommittedStartFailureDisposition {
	var failure *delegateCommittedStartFailureError
	if errors.As(err, &failure) {
		return failure.disposition
	}
	return 0
}

type delegateInputClaim struct {
	lease delegateLease
	token uint64
}

func (c *delegateTreeController) ReserveCreate(actor delegateActor, descriptor delegatestore.Descriptor) (*delegateStartReservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, errDelegateTargetBusy
	}
	parentID := ""
	if actor.lease == nil {
		if actor.rootSessionID != c.rootSessionID {
			return nil, errDelegateNotControllable
		}
	} else {
		if _, _, err := c.admitLeaseLocked(*actor.lease, delegatestore.PhaseRunning); err != nil {
			return nil, err
		}
		parentID = actor.lease.delegateID
	}
	if strings.TrimSpace(descriptor.Task) == "" || strings.TrimSpace(descriptor.AgentType) == "" || !descriptor.Resumable {
		return nil, errDelegateTargetBusy
	}
	if len(descriptor.ResultSchema) > 0 && !json.Valid(descriptor.ResultSchema) {
		return nil, errDelegateTargetBusy
	}
	if !c.reserveCapacityLocked(delegateTurnCapacity) {
		return nil, errTreeAtCapacity
	}
	delegateID := c.newDelegateID()
	childSessionID := identifier.MustNewSessionID()
	descriptor.ParentDelegateID = parentID
	descriptor.OwnerSessionID = c.rootSessionID
	descriptor.ChildSessionID = childSessionID
	descriptor.TranscriptRef = encodeRef("", childSessionID)
	worktreePath := ""
	if descriptor.Isolation == "worktree" {
		worktreeRoot := strings.TrimSpace(descriptor.WorkingDir)
		if worktreeRoot == "" {
			worktreeRoot = c.worktreeRoot
		}
		worktreePath = filepath.Join(worktreeRoot, delegateID)
		descriptor.WorkingDir = worktreePath
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.nextToken++
	transcriptPath := filepath.Join(c.stateDir, sessionsSubdir, childSessionID+".transcript.jsonl")
	storedDescriptor := cloneDelegateStartDescriptor(descriptor)
	reservation := &delegateStartReservation{
		delegateID:     delegateID,
		descriptor:     cloneDelegateStartDescriptor(storedDescriptor),
		transcriptPath: transcriptPath,
		worktreePath:   worktreePath,
	}
	record := &delegateStartRecord{
		receipt:        reservation,
		token:          c.nextToken,
		delegateID:     delegateID,
		generation:     1,
		trigger:        delegatestore.TriggerInitial,
		capacityKind:   delegateTurnCapacity,
		ctx:            ctx,
		cancel:         cancel,
		create:         true,
		descriptor:     storedDescriptor,
		transcriptPath: transcriptPath,
		worktreePath:   worktreePath,
	}
	c.reservations[record.token] = record
	c.evidenceVersion++
	return reservation, nil
}

// ReserveAttention trusts attentionID only after its caller has folded the
// receiver transcript outside c.mu. The locked work authenticates the exact
// resident runtime and reserves drive capacity; it performs no transcript I/O.
func (c *delegateTreeController) ReserveAttention(runtime *Session, attentionID string) (*delegateStartReservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, errDelegateTargetBusy
	}
	if runtime == nil {
		return nil, errDelegateStaleLease
	}
	delegateID, live, err := c.runtimeOwnerLocked(runtime)
	if err != nil {
		return nil, err
	}
	if live == nil {
		return nil, errDelegateStaleLease
	}
	aggregate := c.durable[delegateID]
	if attentionID == "" || aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || live.binding != nil || live.recoveryRequired {
		return nil, errDelegateTargetBusy
	}
	for _, existing := range c.reservations {
		if existing.delegateID == delegateID {
			return nil, errDelegateTargetBusy
		}
	}
	if !c.reserveCapacityLocked(delegateDriveCapacity) {
		return nil, errTreeAtCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.nextToken++
	reservation := &delegateStartReservation{
		delegateID: delegateID,
	}
	record := &delegateStartRecord{
		receipt:      reservation,
		token:        c.nextToken,
		delegateID:   delegateID,
		generation:   aggregate.Generation + 1,
		trigger:      delegatestore.TriggerAttention,
		capacityKind: delegateDriveCapacity,
		ctx:          ctx,
		cancel:       cancel,
		runtime:      runtime,
		attentionID:  attentionID,
	}
	c.reservations[record.token] = record
	c.evidenceVersion++
	return reservation, nil
}

func (c *delegateTreeController) AttachRuntime(lease delegateLease, runtime *Session) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errDelegateTargetBusy
	}
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return err
	}
	if runtime == nil || aggregate.Phase != delegatestore.PhaseRunning || aggregate.PendingStopSeq != 0 || live.recoveryRequired || live.binding.ready {
		return errDelegateTargetBusy
	}
	if live.runtime != nil && live.runtime != runtime || live.binding.runtime != nil && live.binding.runtime != runtime {
		return errDelegateTargetBusy
	}
	ownerID, _, err := c.runtimeOwnerLocked(runtime)
	if err != nil || ownerID != "" && ownerID != lease.delegateID {
		return errDelegateTargetBusy
	}
	live.runtime = runtime
	live.binding.runtime = runtime
	c.evidenceVersion++
	return nil
}

func (c *delegateTreeController) runtimeOwnerLocked(runtime *Session) (string, *delegateLiveState, error) {
	var ownerID string
	var owner *delegateLiveState
	for id, candidate := range c.live {
		if candidate == nil || candidate.runtime != runtime && (candidate.binding == nil || candidate.binding.runtime != runtime) {
			continue
		}
		if owner != nil && ownerID != id {
			return "", nil, errDelegateTargetBusy
		}
		ownerID = id
		owner = candidate
	}
	return ownerID, owner, nil
}

func (c *delegateTreeController) BeginStartInput(lease delegateLease) (delegateInputClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return delegateInputClaim{}, err
	}
	if aggregate.Phase != delegatestore.PhaseRunning || aggregate.PendingStopSeq != 0 || live.recoveryRequired || live.binding.ready || live.binding.runtime == nil || live.binding.inputClaim != 0 || aggregate.Trigger == delegatestore.TriggerAttention {
		return delegateInputClaim{}, errDelegateTargetBusy
	}
	c.nextToken++
	claim := delegateInputClaim{lease: lease, token: c.nextToken}
	c.inputClaims[claim.token] = lease
	live.binding.inputClaim = claim.token
	c.evidenceVersion++
	return claim, nil
}

func (c *delegateTreeController) CompleteStartInput(claim delegateInputClaim, committed bool, failure delegateFinish) (delegateMutationPlans, error) {
	c.mu.Lock()
	var cancel context.CancelFunc
	defer func() {
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}()
	lease, exists := c.inputClaims[claim.token]
	if claim.token == 0 || !exists || lease != claim.lease {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	if live.binding.inputClaim != claim.token {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	delete(c.inputClaims, claim.token)
	live.binding.inputClaim = 0
	if aggregate.Phase == delegatestore.PhaseStopping {
		plans, generationCancel, finishErr := c.finishStoppedStartLocked(lease, live)
		cancel = generationCancel
		return plans, finishErr
	}
	if aggregate.Phase != delegatestore.PhaseRunning || live.recoveryRequired || live.binding.ready || live.binding.runtime == nil || aggregate.Trigger == delegatestore.TriggerAttention {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	if !committed {
		terminal, finish := c.startInputFailureBatch(lease, failure)
		if _, finishErr := c.appendLocked(terminal, finish); finishErr != nil {
			live.recoveryRequired = true
			return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(lease.delegateID)}}, finishErr
		}
		plans, generationCancel := c.generationFinishedPlansLocked(lease, finish.RunFinished.DeliveryID)
		cancel = generationCancel
		return plans, nil
	}
	live.binding.ready = true
	live.activityAt = c.now()
	c.evidenceVersion++
	return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(lease.delegateID)}}, nil
}

func (c *delegateTreeController) FailCommittedStart(lease delegateLease, failure delegateFinish, closeReason string) (delegateMutationPlans, error) {
	c.mu.Lock()
	var cancel context.CancelFunc
	defer func() {
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}()
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	if aggregate.Phase == delegatestore.PhaseStopping {
		plans, generationCancel, finishErr := c.finishStoppedStartLocked(lease, live)
		cancel = generationCancel
		disposition := delegateCommittedStartFailureStopWon
		if finishErr != errDelegateTargetBusy {
			disposition = delegateCommittedStartFailureAppendFailed
		}
		return plans, &delegateCommittedStartFailureError{disposition: disposition, cause: finishErr}
	}
	closeReason = strings.TrimSpace(closeReason)
	if closeReason == "" || aggregate.Phase != delegatestore.PhaseRunning || live.recoveryRequired || live.binding.ready {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	terminal, finish := c.committedStartFailureBatch(lease, failure)
	closure := delegatestore.Event{
		Kind:               delegatestore.EventDelegateResumabilityClosed,
		DelegateID:         lease.delegateID,
		ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: closeReason},
	}
	if _, appendErr := c.appendLocked(terminal, finish, closure); appendErr != nil {
		live.recoveryRequired = true
		return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(lease.delegateID)}}, &delegateCommittedStartFailureError{
			disposition: delegateCommittedStartFailureAppendFailed,
			cause:       appendErr,
		}
	}
	plans, generationCancel := c.generationFinishedPlansLocked(lease, finish.RunFinished.DeliveryID)
	cancel = generationCancel
	return plans, nil
}

func (c *delegateTreeController) finishStoppedStartLocked(lease delegateLease, live *delegateLiveState) (delegateMutationPlans, context.CancelFunc, error) {
	packet := delegateStoppedTerminalPacket()
	deliveryID := delegateDeliveryID(lease.delegateID, lease.generation)
	finish := delegateRunFinishedEvent(
		lease,
		delegatestore.OutcomeStopped,
		delegatestore.DispositionTerminalError,
		"stopped_by_parent",
		c.now(),
		deliveryID,
		&packet,
	)
	if _, finishErr := c.appendLocked(finish); finishErr != nil {
		live.recoveryRequired = true
		plans := delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(lease.delegateID)}}
		return plans, nil, errors.Join(errDelegateTargetBusy, finishErr)
	}
	plans, cancel := c.generationFinishedPlansLocked(lease, deliveryID)
	return plans, cancel, errDelegateTargetBusy
}

func (c *delegateTreeController) ReserveStart(actor delegateActor, delegateID string) (*delegateStartReservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, errDelegateTargetBusy
	}
	if err := c.authorizeMutationLocked(actor, delegateID); err != nil {
		return nil, err
	}
	if actor.lease != nil {
		if _, _, err := c.admitLeaseLocked(*actor.lease, delegatestore.PhaseRunning); err != nil {
			return nil, err
		}
	}
	aggregate := c.durable[delegateID]
	if aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.PendingStopSeq != 0 {
		return nil, errDelegateTargetBusy
	}
	for _, existing := range c.reservations {
		if existing.delegateID == delegateID {
			return nil, errDelegateTargetBusy
		}
	}
	if !c.reserveCapacityLocked(delegateTurnCapacity) {
		return nil, errTreeAtCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.nextToken++
	descriptor := cloneDelegateStartDescriptor(aggregate.Descriptor)
	transcriptPath := filepath.Join(c.stateDir, sessionsSubdir, descriptor.ChildSessionID+".transcript.jsonl")
	worktreePath := ""
	if descriptor.Isolation == "worktree" {
		worktreePath = descriptor.WorkingDir
	}
	reservation := &delegateStartReservation{
		delegateID:     delegateID,
		descriptor:     cloneDelegateStartDescriptor(descriptor),
		transcriptPath: transcriptPath,
		worktreePath:   worktreePath,
	}
	record := &delegateStartRecord{
		receipt:        reservation,
		token:          c.nextToken,
		delegateID:     delegateID,
		generation:     aggregate.Generation + 1,
		trigger:        delegatestore.TriggerOwnerInput,
		capacityKind:   delegateTurnCapacity,
		ctx:            ctx,
		cancel:         cancel,
		descriptor:     descriptor,
		transcriptPath: transcriptPath,
		worktreePath:   worktreePath,
	}
	c.reservations[record.token] = record
	c.evidenceVersion++
	return reservation, nil
}

func (c *delegateTreeController) AbortStart(reservation *delegateStartReservation) error {
	c.mu.Lock()
	record, err := c.reservationRecordLocked(reservation)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	cancel := c.releaseReservationLocked(record)
	c.mu.Unlock()
	cancel()
	return nil
}

func (c *delegateTreeController) reservationRecordLocked(reservation *delegateStartReservation) (*delegateStartRecord, error) {
	if reservation == nil {
		return nil, errDelegateTargetBusy
	}
	for _, record := range c.reservations {
		if record.receipt == reservation {
			return record, nil
		}
	}
	return nil, errDelegateTargetBusy
}

func (c *delegateTreeController) releaseReservationLocked(record *delegateStartRecord) context.CancelFunc {
	delete(c.reservations, record.token)
	if c.stop != nil {
		if _, tracked := c.stop.starts[record.token]; tracked {
			delete(c.stop.starts, record.token)
			c.signalStopProgressLocked()
		}
	}
	c.releaseCapacityLocked(record.capacityKind)
	c.evidenceVersion++
	return record.cancel
}

func (c *delegateTreeController) CommitStart(reservation *delegateStartReservation) (delegateStartCommit, error) {
	c.mu.Lock()
	var cancel context.CancelFunc
	defer func() {
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}()
	record, err := c.reservationRecordLocked(reservation)
	if err != nil {
		return delegateStartCommit{}, errDelegateTargetBusy
	}
	if record.ctx.Err() != nil || c.closing || c.stopCoversLocked(record.delegateID) {
		cancel = c.releaseReservationLocked(record)
		return delegateStartCommit{}, errDelegateTargetBusy
	}
	aggregate := c.durable[record.delegateID]
	if record.create {
		if aggregate != nil || record.generation != 1 {
			cancel = c.releaseReservationLocked(record)
			return delegateStartCommit{}, errDelegateTargetBusy
		}
	} else if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.Generation+1 != record.generation {
		cancel = c.releaseReservationLocked(record)
		return delegateStartCommit{}, errDelegateTargetBusy
	}
	if record.waiter != nil {
		if live := c.live[record.delegateID]; live != nil && live.waiters != nil && live.waiters[record.generation] != nil {
			cancel = c.releaseReservationLocked(record)
			return delegateStartCommit{}, errDelegateTargetBusy
		}
	}
	startedAt := c.now()
	events := []delegatestore.Event{delegateControllerRunStartedEvent(
		record.delegateID,
		record.generation,
		record.trigger,
		startedAt,
	)}
	if record.create {
		events = append([]delegatestore.Event{{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: record.delegateID,
			Created:    &delegatestore.DelegateCreated{Descriptor: record.descriptor},
		}}, events...)
	}
	_, err = c.appendLocked(events...)
	if err != nil {
		cancel = c.releaseReservationLocked(record)
		return delegateStartCommit{}, err
	}
	delete(c.reservations, record.token)
	if c.stop != nil {
		if _, tracked := c.stop.starts[record.token]; tracked {
			delete(c.stop.starts, record.token)
			c.signalStopProgressLocked()
		}
	}
	live := c.live[record.delegateID]
	if live == nil {
		live = &delegateLiveState{}
		c.live[record.delegateID] = live
	}
	lease := delegateLease{delegateID: record.delegateID, generation: record.generation}
	live.binding = &delegateRuntimeBinding{
		lease:   lease,
		runtime: record.runtime,
		cancel:  record.cancel,
		ready:   record.trigger == delegatestore.TriggerAttention,
	}
	if record.trigger == delegatestore.TriggerAttention {
		live.attentionIDs = []string{record.attentionID}
	}
	if record.runtime != nil {
		live.runtime = record.runtime
	}
	if record.waiter != nil {
		if live.waiters == nil {
			live.waiters = make(map[uint64]*delegateInlineWaiter)
		}
		live.waiters[record.generation] = record.waiter
	}
	live.activityAt = startedAt
	c.evidenceVersion++
	return delegateStartCommit{
		lease:          lease,
		plan:           c.capturedPlanLocked(record.delegateID),
		ctx:            record.ctx,
		descriptor:     cloneDelegateStartDescriptor(record.descriptor),
		transcriptPath: record.transcriptPath,
		worktreePath:   record.worktreePath,
	}, nil
}

func (c *delegateTreeController) releaseGenerationLocked(lease delegateLease) context.CancelFunc {
	live := c.live[lease.delegateID]
	if live == nil || live.binding == nil || live.binding.lease != lease {
		return nil
	}
	cancel := live.binding.cancel
	if live.binding.inputClaim != 0 {
		delete(c.inputClaims, live.binding.inputClaim)
	}
	if live.binding.runtime != nil {
		live.runtime = live.binding.runtime
	}
	live.binding = nil
	live.pendingSteers = nil
	live.attentionIDs = nil
	live.recoveryRequired = false
	if c.durable[lease.delegateID].Trigger == delegatestore.TriggerAttention {
		c.releaseCapacityLocked(delegateDriveCapacity)
	} else {
		c.releaseCapacityLocked(delegateTurnCapacity)
	}
	return cancel
}

func (c *delegateTreeController) beginRuntimeBoundary(lease delegateLease) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	return err
}

func (c *delegateTreeController) inputPersistFailureBatch(lease delegateLease, inputErr error) (delegatestore.Event, delegatestore.Event) {
	message := "input persistence failed"
	if inputErr != nil {
		message += ": " + inputErr.Error()
	}
	if len(message) > 512 {
		message = message[:512]
	}
	raw, _ := json.Marshal(message)
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: raw}
	return terminalFinishBatch(lease, delegatestore.OutcomeFailed, "input_persist_failed", c.now(), packet)
}

func (c *delegateTreeController) startInputFailureBatch(lease delegateLease, failure delegateFinish) (delegatestore.Event, delegatestore.Event) {
	failure = normalizeDelegateStartFailure(failure, "input_persist_failed", "input persistence failed", c.now())
	return terminalFinishBatch(lease, failure.outcome, failure.reason, failure.endedAt, *failure.packet)
}

func (c *delegateTreeController) committedStartFailureBatch(lease delegateLease, failure delegateFinish) (delegatestore.Event, delegatestore.Event) {
	failure = normalizeDelegateStartFailure(failure, "construction_failed", "delegate construction failed", c.now())
	return terminalFinishBatch(lease, failure.outcome, failure.reason, failure.endedAt, *failure.packet)
}

func normalizeDelegateStartFailure(failure delegateFinish, fallbackReason, fallbackMessage string, now time.Time) delegateFinish {
	if failure.outcome == "" {
		failure.outcome = delegatestore.OutcomeFailed
	}
	if strings.TrimSpace(failure.reason) == "" {
		failure.reason = fallbackReason
	}
	if failure.endedAt.IsZero() {
		failure.endedAt = now
	}
	if failure.packet == nil {
		message, _ := json.Marshal(fallbackMessage)
		failure.packet = &delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: message}
	}
	return failure
}

func terminalFinishBatch(lease delegateLease, status delegatestore.OutcomeStatus, reason string, endedAt time.Time, packet delegatestore.TerminalPacket) (delegatestore.Event, delegatestore.Event) {
	return delegatestore.Event{
			Kind:       delegatestore.EventDelegateTerminalPrepared,
			DelegateID: lease.delegateID,
			TerminalPrepared: &delegatestore.TerminalPrepared{
				Generation: lease.generation,
				Packet:     packet,
			},
		}, delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: lease.delegateID,
			RunFinished: &delegatestore.RunFinished{
				Generation: lease.generation,
				Outcome: delegatestore.Outcome{
					Status:  status,
					Reason:  reason,
					EndedAt: endedAt,
				},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateDeliveryID(lease.delegateID, lease.generation),
			},
		}
}

func (c *delegateTreeController) reserveCapacityLocked(kind delegateCapacityKind) bool {
	if kind == delegateDriveCapacity {
		if c.driveLimit > 0 && c.drivesInUse >= c.driveLimit {
			return false
		}
		c.drivesInUse++
		return true
	}
	if c.turnLimit > 0 && c.turnsInUse >= c.turnLimit {
		return false
	}
	c.turnsInUse++
	return true
}

func (c *delegateTreeController) releaseCapacityLocked(kind delegateCapacityKind) {
	if kind == delegateDriveCapacity {
		if c.drivesInUse > 0 {
			c.drivesInUse--
		}
		return
	}
	if c.turnsInUse > 0 {
		c.turnsInUse--
	}
}

func cloneDelegateStartDescriptor(descriptor delegatestore.Descriptor) delegatestore.Descriptor {
	clone := descriptor
	clone.FrozenToolNames = append([]string(nil), descriptor.FrozenToolNames...)
	clone.FrozenSkillNames = append([]string(nil), descriptor.FrozenSkillNames...)
	clone.FrozenSkillBodies = append([]string(nil), descriptor.FrozenSkillBodies...)
	clone.ResultSchema = append(json.RawMessage(nil), descriptor.ResultSchema...)
	clone.ExplicitToolGrants = append([]string(nil), descriptor.ExplicitToolGrants...)
	clone.Provenance = provenance.Clone(descriptor.Provenance)
	if descriptor.Sandbox != nil {
		sandbox := *descriptor.Sandbox
		if descriptor.Sandbox.Network != nil {
			network := *descriptor.Sandbox.Network
			sandbox.Network = &network
		}
		sandbox.DenylistAdd = append([]string(nil), descriptor.Sandbox.DenylistAdd...)
		sandbox.DenylistRemove = append([]string(nil), descriptor.Sandbox.DenylistRemove...)
		sandbox.ExtraWritableRoots = append([]string(nil), descriptor.Sandbox.ExtraWritableRoots...)
		sandbox.ExtraReadRoots = append([]string(nil), descriptor.Sandbox.ExtraReadRoots...)
		clone.Sandbox = &sandbox
	}
	return clone
}

func delegateControllerRunStartedEvent(id string, generation uint64, trigger delegatestore.RunTrigger, startedAt time.Time) delegatestore.Event {
	return delegatestore.Event{
		Kind:       delegatestore.EventDelegateRunStarted,
		DelegateID: id,
		RunStarted: &delegatestore.RunStarted{
			Generation: generation,
			Trigger:    trigger,
			StartedAt:  startedAt,
		},
	}
}
