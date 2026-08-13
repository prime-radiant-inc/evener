package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/identifier"
)

type delegateCapacityKind uint8

const (
	delegateTurnCapacity delegateCapacityKind = iota
	delegateDriveCapacity
)

type delegateStartReservation struct {
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
}

type delegateStartCommit struct {
	lease delegateLease
	plan  delegateUpdatePlan
}

type delegateGenerationFinish struct {
	status delegatestore.OutcomeStatus
	reason string
}

func (c *delegateTreeController) ReserveCreate(actor delegateActor, descriptor delegatestore.Descriptor) (*delegateStartReservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	delegateID := jobstore.NewDelegateID()
	childSessionID := identifier.MustNewSessionID()
	descriptor.ParentDelegateID = parentID
	descriptor.OwnerSessionID = c.rootSessionID
	descriptor.ChildSessionID = childSessionID
	descriptor.TranscriptRef = encodeRef("", childSessionID)
	worktreePath := ""
	if descriptor.Isolation == "worktree" {
		worktreePath = filepath.Join(c.worktreeRoot, delegateID)
		descriptor.WorkingDir = worktreePath
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.nextToken++
	reservation := &delegateStartReservation{
		token:          c.nextToken,
		delegateID:     delegateID,
		generation:     1,
		trigger:        delegatestore.TriggerInitial,
		capacityKind:   delegateTurnCapacity,
		ctx:            ctx,
		cancel:         cancel,
		create:         true,
		descriptor:     descriptor,
		transcriptPath: filepath.Join(c.stateDir, sessionsSubdir, childSessionID+".transcript.jsonl"),
		worktreePath:   worktreePath,
	}
	c.reservations[reservation.token] = reservation
	return reservation, nil
}

// ReserveAttention trusts attentionID only after its caller has folded the
// receiver transcript outside c.mu. The locked work authenticates the exact
// resident runtime and reserves drive capacity; it performs no transcript I/O.
func (c *delegateTreeController) ReserveAttention(runtime *Session, attentionID string) (*delegateStartReservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if runtime == nil {
		return nil, errDelegateStaleLease
	}
	var delegateID string
	var live *delegateLiveState
	for id, candidate := range c.live {
		if candidate != nil && candidate.runtime == runtime {
			delegateID = id
			live = candidate
			break
		}
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
	c.reservations[reservation.token] = reservation
	return reservation, nil
}

func (c *delegateTreeController) AttachRuntime(lease delegateLease, runtime *Session) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return err
	}
	if runtime == nil || aggregate.Phase != delegatestore.PhaseRunning || aggregate.PendingStopSeq != 0 || live.recoveryRequired || live.binding.ready {
		return errDelegateTargetBusy
	}
	if live.binding.runtime != nil && live.binding.runtime != runtime {
		return errDelegateTargetBusy
	}
	live.runtime = runtime
	live.binding.runtime = runtime
	return nil
}

// AdmitStartInput is the one narrow controller-to-transcript lock boundary.
// admitInput must append only to the child transcript and must not call back
// into the controller while c.mu is held.
func (c *delegateTreeController) AdmitStartInput(lease delegateLease, admitInput func() error) (delegateUpdatePlan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return delegateUpdatePlan{}, err
	}
	if admitInput == nil || aggregate.Phase != delegatestore.PhaseRunning || aggregate.PendingStopSeq != 0 || live.recoveryRequired || live.binding.ready || live.binding.runtime == nil || aggregate.Trigger == delegatestore.TriggerAttention {
		return delegateUpdatePlan{}, errDelegateTargetBusy
	}
	if err := admitInput(); err != nil {
		terminal, finish := c.inputPersistFailureBatch(lease, err)
		if _, finishErr := c.appendLocked(terminal, finish); finishErr != nil {
			live.recoveryRequired = true
			return c.capturedPlanLocked(lease.delegateID), errors.Join(err, finishErr)
		}
		c.releaseGenerationLocked(lease)
		return c.capturedPlanLocked(lease.delegateID), err
	}
	live.binding.ready = true
	live.activityAt = c.now()
	return c.capturedPlanLocked(lease.delegateID), nil
}

func (c *delegateTreeController) BeginModelRequest(lease delegateLease) error {
	return c.beginRuntimeBoundary(lease)
}

func (c *delegateTreeController) BeginToolExecution(lease delegateLease) error {
	return c.beginRuntimeBoundary(lease)
}

func (c *delegateTreeController) ReserveStart(actor delegateActor, delegateID string) (*delegateStartReservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	reservation := &delegateStartReservation{
		token:        c.nextToken,
		delegateID:   delegateID,
		generation:   aggregate.Generation + 1,
		trigger:      delegatestore.TriggerOwnerInput,
		capacityKind: delegateTurnCapacity,
		ctx:          ctx,
		cancel:       cancel,
	}
	c.reservations[reservation.token] = reservation
	return reservation, nil
}

func (c *delegateTreeController) AbortStart(reservation *delegateStartReservation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.abortReservationLocked(reservation)
}

func (c *delegateTreeController) abortReservationLocked(reservation *delegateStartReservation) error {
	if reservation == nil || c.reservations[reservation.token] != reservation {
		return errDelegateTargetBusy
	}
	delete(c.reservations, reservation.token)
	reservation.cancel()
	c.releaseCapacityLocked(reservation.capacityKind)
	return nil
}

func (c *delegateTreeController) CommitStart(reservation *delegateStartReservation) (delegateStartCommit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reservation == nil || c.reservations[reservation.token] != reservation || reservation.ctx.Err() != nil {
		return delegateStartCommit{}, errDelegateTargetBusy
	}
	aggregate := c.durable[reservation.delegateID]
	if reservation.create {
		if aggregate != nil || reservation.generation != 1 {
			_ = c.abortReservationLocked(reservation)
			return delegateStartCommit{}, errDelegateTargetBusy
		}
	} else if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.Generation+1 != reservation.generation {
		_ = c.abortReservationLocked(reservation)
		return delegateStartCommit{}, errDelegateTargetBusy
	}
	startedAt := c.now()
	events := []delegatestore.Event{delegateControllerRunStartedEvent(
		reservation.delegateID,
		reservation.generation,
		reservation.trigger,
		startedAt,
	)}
	if reservation.create {
		events = append([]delegatestore.Event{{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: reservation.delegateID,
			Created:    &delegatestore.DelegateCreated{Descriptor: reservation.descriptor},
		}}, events...)
	}
	_, err := c.appendLocked(events...)
	if err != nil {
		_ = c.abortReservationLocked(reservation)
		return delegateStartCommit{}, err
	}
	delete(c.reservations, reservation.token)
	live := c.live[reservation.delegateID]
	if live == nil {
		live = &delegateLiveState{}
		c.live[reservation.delegateID] = live
	}
	lease := delegateLease{delegateID: reservation.delegateID, generation: reservation.generation}
	live.binding = &delegateRuntimeBinding{
		lease:   lease,
		runtime: reservation.runtime,
		cancel:  reservation.cancel,
		ready:   reservation.trigger == delegatestore.TriggerAttention,
	}
	if reservation.runtime != nil {
		live.runtime = reservation.runtime
	}
	live.activityAt = startedAt
	return delegateStartCommit{lease: lease, plan: c.capturedPlanLocked(reservation.delegateID)}, nil
}

func (c *delegateTreeController) FinishGeneration(lease delegateLease, finish delegateGenerationFinish) (delegateUpdatePlan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate, _, err := c.exactLeaseLocked(lease)
	if err != nil {
		return delegateUpdatePlan{}, err
	}
	message, _ := json.Marshal(finish.reason)
	packet := delegatestore.TerminalPacket{
		Kind:    delegatestore.PacketTerminalError,
		Message: message,
	}
	terminal, finished := terminalFinishBatch(lease, finish.status, finish.reason, c.now(), packet)
	if err := c.appendTerminalFinishLocked(aggregate.Phase, terminal, finished); err != nil {
		return delegateUpdatePlan{}, err
	}
	c.releaseGenerationLocked(lease)
	return c.capturedPlanLocked(lease.delegateID), nil
}

func (c *delegateTreeController) releaseGenerationLocked(lease delegateLease) {
	live := c.live[lease.delegateID]
	if live == nil || live.binding == nil || live.binding.lease != lease {
		return
	}
	live.binding.cancel()
	if live.binding.runtime != nil {
		live.runtime = live.binding.runtime
	}
	live.binding = nil
	live.recoveryRequired = false
	if c.durable[lease.delegateID].Trigger == delegatestore.TriggerAttention {
		c.releaseCapacityLocked(delegateDriveCapacity)
	} else {
		c.releaseCapacityLocked(delegateTurnCapacity)
	}
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
				DeliveryID:  fmt.Sprintf("%s/delivery/%d", lease.delegateID, lease.generation),
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
