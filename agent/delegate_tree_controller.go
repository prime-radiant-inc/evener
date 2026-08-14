package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/identifier"
)

var (
	errDelegateNotControllable = errors.New("not_controllable: delegate")
	errDelegateStaleLease      = errors.New("stale_delegate_lease")
	errDelegateTargetBusy      = errors.New("target_busy")
)

type delegateLifecycle string

const (
	delegateLifecycleRunning delegateLifecycle = "running"
	delegateLifecycleIdle    delegateLifecycle = "idle"
)

type delegateTreeControllerConfig struct {
	store         *delegatestore.Store
	rootSessionID string
	stateDir      string
	worktreeRoot  string
	turnLimit     int
	driveLimit    int
	now           func() time.Time
	newDelegateID func() string
}

type delegateTreeController struct {
	mu sync.Mutex

	store   *delegatestore.Store
	durable delegatestore.State
	live    map[string]*delegateLiveState

	rootSessionID   string
	stateDir        string
	worktreeRoot    string
	now             func() time.Time
	newDelegateID   func() string
	turnLimit       int
	driveLimit      int
	turnsInUse      int
	drivesInUse     int
	nextToken       uint64
	reservations    map[uint64]*delegateStartRecord
	inputClaims     map[uint64]delegateLease
	work            map[uint64]*delegateShellWork
	deliveries      map[uint64]*delegateDeliveryAdmission
	deliveryClaims  map[string]*delegateDeliveryClaim
	stop            *delegateStopState
	evidenceVersion uint64
	closing         bool
	reconcileOrder  []delegateLease
	emitUpdate      func(delegateUpdatePlan)
}

type delegateActor struct {
	rootSessionID string
	lease         *delegateLease
}

type delegateLease struct {
	delegateID string
	generation uint64
}

type delegateRuntimeBinding struct {
	lease      delegateLease
	runtime    *Session
	cancel     context.CancelFunc
	ready      bool
	inputClaim uint64
}

type delegateLiveState struct {
	runtime          *Session
	binding          *delegateRuntimeBinding
	pendingSteers    []delegateSteeringAdmission
	attentionIDs     []string
	waiters          map[uint64]*delegateInlineWaiter
	recoveryRequired bool
	activityAt       time.Time
}

type delegateSnapshot struct {
	id                 string
	parentID           string
	lifecycle          delegateLifecycle
	phase              delegatestore.Phase
	resumable          bool
	revision           uint64
	transcriptRef      string
	notResumableReason string
	latestActivityAt   time.Time
	lastOutcome        *delegatestore.Outcome
}

type delegateUpdatePlan struct {
	rows []delegateSnapshot
}

type delegateMutationPlans struct {
	updates      []delegateUpdatePlan
	deliveries   []delegateDeliveryPlan
	attention    []delegateAttentionCleanupPlan
	shellRepairs []delegateShellRepairPlan
}

func openDelegateTreeController(cfg delegateTreeControllerConfig) (*delegateTreeController, error) {
	if cfg.store == nil {
		return nil, errors.New("delegate controller store is nil")
	}
	if cfg.rootSessionID == "" {
		return nil, errors.New("delegate controller root session id is empty")
	}
	if cfg.now == nil {
		cfg.now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.turnLimit <= 0 {
		cfg.turnLimit = defaultMaxConcurrentDelegateTurns
	}
	if cfg.driveLimit <= 0 {
		cfg.driveLimit = defaultMaxConcurrentDriveTurns
	}
	if cfg.newDelegateID == nil {
		cfg.newDelegateID = identifier.MustNewDelegateID
	}
	events, err := cfg.store.Load()
	if err != nil {
		return nil, err
	}
	durable, err := delegatestore.Fold(events)
	if err != nil {
		return nil, err
	}
	c := &delegateTreeController{
		store:          cfg.store,
		durable:        durable,
		live:           make(map[string]*delegateLiveState),
		rootSessionID:  cfg.rootSessionID,
		stateDir:       cfg.stateDir,
		worktreeRoot:   cfg.worktreeRoot,
		now:            cfg.now,
		turnLimit:      cfg.turnLimit,
		driveLimit:     cfg.driveLimit,
		newDelegateID:  cfg.newDelegateID,
		reservations:   make(map[uint64]*delegateStartRecord),
		inputClaims:    make(map[uint64]delegateLease),
		work:           make(map[uint64]*delegateShellWork),
		deliveries:     make(map[uint64]*delegateDeliveryAdmission),
		deliveryClaims: make(map[string]*delegateDeliveryClaim),
		reconcileOrder: delegateOpenRunOrder(events, durable),
	}
	if err := c.restorePendingStop(events); err != nil {
		return nil, err
	}
	return c, nil
}

func rootDelegateActor(rootSessionID string) delegateActor {
	return delegateActor{rootSessionID: rootSessionID}
}

func (c *delegateTreeController) appendLocked(events ...delegatestore.Event) ([]delegatestore.Event, error) {
	appended, next, err := c.store.AppendBatch(c.durable, events)
	if err != nil {
		return nil, err
	}
	c.durable = next
	return appended, nil
}

func (c *delegateTreeController) authorizeMutationLocked(actor delegateActor, targetID string) error {
	target := c.durable[targetID]
	if target == nil {
		return errDelegateNotControllable
	}
	if target.Descriptor.ParentDelegateID == "" {
		if actor.lease == nil && actor.rootSessionID == c.rootSessionID && target.Descriptor.OwnerSessionID == c.rootSessionID {
			return nil
		}
		return errDelegateNotControllable
	}
	if actor.lease == nil {
		return errDelegateNotControllable
	}
	if _, _, err := c.exactLeaseLocked(*actor.lease); err != nil {
		return err
	}
	if target.Descriptor.ParentDelegateID != actor.lease.delegateID {
		return errDelegateNotControllable
	}
	return nil
}

func (c *delegateTreeController) AuthorizeMutation(actor delegateActor, targetID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authorizeMutationLocked(actor, targetID)
}

func (c *delegateTreeController) exactLeaseLocked(lease delegateLease) (*delegatestore.Aggregate, *delegateLiveState, error) {
	aggregate := c.durable[lease.delegateID]
	if aggregate == nil || aggregate.Generation != lease.generation || !aggregate.CurrentRunOpen {
		return nil, nil, fmt.Errorf("%w: %s generation %d", errDelegateStaleLease, lease.delegateID, lease.generation)
	}
	live := c.live[lease.delegateID]
	if live == nil || live.binding == nil || live.binding.lease != lease {
		return nil, nil, fmt.Errorf("%w: %s generation %d has no exact binding", errDelegateStaleLease, lease.delegateID, lease.generation)
	}
	return aggregate, live, nil
}

func (c *delegateTreeController) admitLeaseLocked(lease delegateLease, phases ...delegatestore.Phase) (*delegatestore.Aggregate, *delegateLiveState, error) {
	if c.closing {
		return nil, nil, errDelegateTargetBusy
	}
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return nil, nil, err
	}
	if live.recoveryRequired || aggregate.PendingStopSeq != 0 || !aggregate.Resumable || live.binding == nil || !live.binding.ready {
		return nil, nil, errDelegateTargetBusy
	}
	for ancestorID := aggregate.Descriptor.ParentDelegateID; ancestorID != ""; {
		ancestor := c.durable[ancestorID]
		if ancestor == nil || ancestor.Phase == delegatestore.PhaseClosed || ancestor.PendingStopSeq != 0 || !ancestor.Resumable {
			return nil, nil, errDelegateTargetBusy
		}
		ancestorID = ancestor.Descriptor.ParentDelegateID
	}
	for _, phase := range phases {
		if aggregate.Phase == phase {
			return aggregate, live, nil
		}
	}
	return nil, nil, errDelegateTargetBusy
}

func (c *delegateTreeController) Snapshot() delegateUpdatePlan {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.durable))
	for id := range c.durable {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := make([]delegateSnapshot, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, c.captureDelegateSnapshotLocked(id))
	}
	return delegateUpdatePlan{rows: rows}
}

func (c *delegateTreeController) emitDelegateUpdate(plan delegateUpdatePlan) {
	if c == nil {
		return
	}
	c.mu.Lock()
	emit := c.emitUpdate
	c.mu.Unlock()
	if emit != nil {
		emit(plan)
	}
}

func (c *delegateTreeController) emitDelegateUpdates(plans delegateMutationPlans) {
	for _, plan := range plans.updates {
		c.emitDelegateUpdate(plan)
	}
}

func (c *delegateTreeController) capturedPlanLocked(id string) delegateUpdatePlan {
	return delegateUpdatePlan{rows: []delegateSnapshot{c.captureDelegateSnapshotLocked(id)}}
}

func (c *delegateTreeController) captureDelegateSnapshotLocked(id string) delegateSnapshot {
	snapshot := captureDelegateSnapshot(c.durable[id])
	if live := c.live[id]; live != nil && live.activityAt.After(snapshot.latestActivityAt) {
		snapshot.latestActivityAt = live.activityAt
	}
	return snapshot
}

func captureDelegateSnapshot(aggregate *delegatestore.Aggregate) delegateSnapshot {
	lifecycle := delegateLifecycleRunning
	if aggregate.Phase == delegatestore.PhaseIdle || aggregate.Phase == delegatestore.PhaseClosed {
		lifecycle = delegateLifecycleIdle
	}
	var outcome *delegatestore.Outcome
	if aggregate.LatestOutcome != nil {
		value := *aggregate.LatestOutcome
		outcome = &value
	}
	return delegateSnapshot{
		id:                 aggregate.DelegateID,
		parentID:           aggregate.Descriptor.ParentDelegateID,
		lifecycle:          lifecycle,
		phase:              aggregate.Phase,
		resumable:          aggregate.Resumable,
		revision:           aggregate.ProjectionRevision,
		transcriptRef:      aggregate.Descriptor.TranscriptRef,
		notResumableReason: aggregate.NotResumableReason,
		latestActivityAt:   aggregate.LatestActivityAt,
		lastOutcome:        outcome,
	}
}

func (c *delegateTreeController) capacityInUse() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turnsInUse, c.drivesInUse
}
