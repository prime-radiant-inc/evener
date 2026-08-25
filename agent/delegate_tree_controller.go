package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
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
	store               *delegatestore.Store
	rootRuntime         *Session
	rootSessionID       string
	stateDir            string
	worktreeRoot        string
	turnLimit           int
	driveLimit          int
	maxRetainedTerminal int
	now                 func() time.Time
	newDelegateID       func() string
	attentionOpen       delegateAttentionWriterOpener
}

type delegateTreeController struct {
	mu sync.Mutex

	store       *delegatestore.Store
	durable     delegatestore.State
	live        map[string]*delegateLiveState
	rootRuntime *Session

	rootSessionID       string
	stateDir            string
	worktreeRoot        string
	now                 func() time.Time
	newDelegateID       func() string
	turnLimit           int
	driveLimit          int
	maxRetainedTerminal int
	turnsInUse          int
	drivesInUse         int
	nextToken           uint64
	reservations        map[uint64]*delegateStartRecord
	inputClaims         map[uint64]delegateLease
	steeringClaims      map[uint64]*delegateSteeringClaim
	modelClaims         map[uint64]*delegateModelRequestClaim
	settlementClaims    map[uint64]*delegateSettlementClaim
	work                map[uint64]*delegateShellWork
	deliveries          map[uint64]*delegateDeliveryAdmission
	deliveryClaims      map[string]*delegateDeliveryClaim
	quietClaims         map[uint64]*delegateQuietAttentionClaim
	attentionWakeIDs    map[string]map[string]struct{}
	watchEnqueues       map[uint64]*delegateWatchReceipt
	watchDeliveries     map[uint64]*delegateWatchReceipt
	reclamations        map[uint64]*delegateRuntimeReclamationClaim
	reclaiming          map[string]uint64
	stop                *delegateStopState
	stopDriver          *delegateStopDriver
	evidenceVersion     uint64
	closing             bool
	reconcileOrder      []delegateLease
	runStarts           map[delegateLease]delegatestore.RunTrigger
	owedAdmission       bool
	emitUpdate          func(delegateUpdatePlan)
	attentionOpen       delegateAttentionWriterOpener
}

type delegateActor struct {
	rootSessionID string
	lease         *delegateLease
}

// describe renders the actor as human-readable provenance for a cancellation
// report (kata tpb0): the caller is always either the tree's root session or
// the exact parent delegate (authorizeMutationLocked admits no other caller),
// so this is a complete account of "who cancelled this" for job_stop's own
// response. Empty for the zero value (no admitted actor).
func (a delegateActor) describe() string {
	if a.lease != nil {
		return "delegate " + a.lease.delegateID
	}
	if a.rootSessionID != "" {
		return "root session " + a.rootSessionID
	}
	return ""
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
	// finalizationRecoveryRequired prevents a failed runner finalizer from
	// retrying around the stop-only external-state recovery path.
	finalizationRecoveryRequired bool
	// recoveryRunnerPending fences stop recovery until the exact runner that
	// encountered a finalization failure has completed all retained-runtime writes.
	recoveryRunnerPending bool
	activityAt            time.Time
	quietSequence         uint64
	quietNotified         bool
	quietClaim            *delegateQuietAttentionClaim
}

type delegateSnapshot struct {
	id                 string
	parentID           string
	descriptor         delegatestore.Descriptor
	generation         uint64
	lifecycle          delegateLifecycle
	phase              delegatestore.Phase
	currentRunOpen     bool
	runStartedAt       time.Time
	resumable          bool
	needsAttention     bool
	revision           uint64
	transcriptRef      string
	notResumableReason string
	latestActivityAt   time.Time
	// pendingStopSeq is the sequence of a subtree stop admitted against this
	// delegate and not yet completed; 0 when no stop is outstanding. It is what
	// distinguishes a delegate that was ASKED to stop from one that has.
	pendingStopSeq uint64
	// pendingStopAt is WHEN that stop was requested, and zero whenever
	// pendingStopSeq is (or when the journal predates the field). A delegate
	// under a pending stop cannot report activity at all — admitLeaseLocked
	// rejects on PendingStopSeq — so time-since-request is the only honest
	// measure of how long a stop has gone unanswered.
	pendingStopAt time.Time
	lastOutcome   *delegatestore.Outcome
	latestPacket  *delegatestore.TerminalPacket
}

// stableDelegateWorktreeSnapshot is the process-local read model used by
// worktree guards and cleanup. Descriptor and lifecycle authority come only
// from the stable delegate controller; shell jobs remain in the job store.
type stableDelegateWorktreeSnapshot struct {
	delegateID         string
	descriptor         delegatestore.Descriptor
	phase              delegatestore.Phase
	resumable          bool
	notResumableReason string
	currentRunOpen     bool
	pendingStopSeq     uint64
	runtime            *Session
	active             bool
}

type delegateUpdatePlan struct {
	rows []delegateSnapshot
}

type delegateMutationPlans struct {
	updates               []delegateUpdatePlan
	deliveries            []delegateDeliveryPlan
	attention             []delegateAttentionCleanupPlan
	attentionFinalization *delegateSettlementClaim
	shellRepairs          []delegateShellRepairPlan
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
	if cfg.maxRetainedTerminal <= 0 {
		cfg.maxRetainedTerminal = defaultMaxRetainedTerminal
	}
	if cfg.newDelegateID == nil {
		cfg.newDelegateID = identifier.MustNewDelegateID
	}
	if cfg.attentionOpen == nil {
		cfg.attentionOpen = transcript.OpenWriterForSession
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
		store:               cfg.store,
		durable:             durable,
		live:                make(map[string]*delegateLiveState),
		rootRuntime:         cfg.rootRuntime,
		rootSessionID:       cfg.rootSessionID,
		stateDir:            cfg.stateDir,
		worktreeRoot:        cfg.worktreeRoot,
		now:                 cfg.now,
		turnLimit:           cfg.turnLimit,
		driveLimit:          cfg.driveLimit,
		maxRetainedTerminal: cfg.maxRetainedTerminal,
		newDelegateID:       cfg.newDelegateID,
		attentionOpen:       cfg.attentionOpen,
		reservations:        make(map[uint64]*delegateStartRecord),
		inputClaims:         make(map[uint64]delegateLease),
		steeringClaims:      make(map[uint64]*delegateSteeringClaim),
		modelClaims:         make(map[uint64]*delegateModelRequestClaim),
		settlementClaims:    make(map[uint64]*delegateSettlementClaim),
		work:                make(map[uint64]*delegateShellWork),
		deliveries:          make(map[uint64]*delegateDeliveryAdmission),
		deliveryClaims:      make(map[string]*delegateDeliveryClaim),
		quietClaims:         make(map[uint64]*delegateQuietAttentionClaim),
		attentionWakeIDs:    make(map[string]map[string]struct{}),
		watchEnqueues:       make(map[uint64]*delegateWatchReceipt),
		watchDeliveries:     make(map[uint64]*delegateWatchReceipt),
		reclamations:        make(map[uint64]*delegateRuntimeReclamationClaim),
		reclaiming:          make(map[string]uint64),
		reconcileOrder:      delegateOpenRunOrder(events, durable),
		runStarts:           delegateRunStartIndex(events),
		owedAdmission:       true,
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
	for _, event := range events {
		if event.ResumabilityClosed != nil && c.attentionStartBlockerLocked(event.DelegateID, true) != nil {
			return nil, errDelegateTargetBusy
		}
	}
	appended, next, err := c.store.AppendBatch(c.durable, events)
	if err != nil {
		return nil, err
	}
	c.durable = next
	for _, event := range appended {
		if event.RunStarted != nil {
			c.runStarts[delegateLease{delegateID: event.DelegateID, generation: event.RunStarted.Generation}] = event.RunStarted.Trigger
		}
	}
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

// stableWorktreeSnapshots returns immutable descriptor/lifecycle rows for all
// stable worktree delegates. It intentionally does not consult jobs.jsonl.
func (c *delegateTreeController) stableWorktreeSnapshots() []stableDelegateWorktreeSnapshot {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]stableDelegateWorktreeSnapshot, 0, len(c.durable))
	for id, aggregate := range c.durable {
		if aggregate == nil || aggregate.Descriptor.Isolation != "worktree" {
			continue
		}
		rows = append(rows, c.stableWorktreeSnapshotLocked(id, aggregate))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].delegateID < rows[j].delegateID })
	return rows
}

// ownedStableWorktreeSnapshots returns only direct worktree children of owner.
// ParentDelegateID is the direct-owner relation; OwnerSessionID authenticates
// root-owned rows against this controller's root session.
func (c *delegateTreeController) ownedStableWorktreeSnapshots(owner *Session) []stableDelegateWorktreeSnapshot {
	if c == nil || owner == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]stableDelegateWorktreeSnapshot, 0)
	for id, aggregate := range c.durable {
		if aggregate == nil || aggregate.Descriptor.Isolation != "worktree" || !c.stableDelegateOwnedBySessionLocked(owner, aggregate) {
			continue
		}
		rows = append(rows, c.stableWorktreeSnapshotLocked(id, aggregate))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].delegateID < rows[j].delegateID })
	return rows
}

func (c *delegateTreeController) stableWorktreeSnapshotForOwner(owner *Session, delegateID string) (stableDelegateWorktreeSnapshot, error) {
	if c == nil || owner == nil {
		return stableDelegateWorktreeSnapshot{}, errDelegateNotControllable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return stableDelegateWorktreeSnapshot{}, errDelegateNotControllable
	}
	if !c.stableDelegateOwnedBySessionLocked(owner, aggregate) {
		return stableDelegateWorktreeSnapshot{}, errDelegateNotControllable
	}
	if aggregate.Descriptor.Isolation != "worktree" {
		return stableDelegateWorktreeSnapshot{}, fmt.Errorf("delegate %s is not worktree-isolated", delegateID)
	}
	return c.stableWorktreeSnapshotLocked(delegateID, aggregate), nil
}

func (c *delegateTreeController) stableWorktreeSnapshotLocked(delegateID string, aggregate *delegatestore.Aggregate) stableDelegateWorktreeSnapshot {
	row := stableDelegateWorktreeSnapshot{
		delegateID:         delegateID,
		descriptor:         cloneDelegateStartDescriptor(aggregate.Descriptor),
		phase:              aggregate.Phase,
		resumable:          aggregate.Resumable,
		notResumableReason: aggregate.NotResumableReason,
		currentRunOpen:     aggregate.CurrentRunOpen,
		pendingStopSeq:     aggregate.PendingStopSeq,
	}
	if live := c.live[delegateID]; live != nil {
		row.runtime = live.runtime
	}
	members := c.subtreeMembersLocked(delegateID)
	row.active = c.stableWorktreeSubtreeActiveLocked(members) || c.runtimeReclamationIntersectsProcessWorkLocked(members)
	return row
}

func (c *delegateTreeController) stableWorktreeSubtreeActiveLocked(members map[string]struct{}) bool {
	for id := range members {
		aggregate := c.durable[id]
		if aggregate == nil {
			continue
		}
		if aggregate.CurrentRunOpen || aggregate.PendingStopSeq != 0 || aggregate.Phase == delegatestore.PhaseRunning || aggregate.Phase == delegatestore.PhaseSettling || aggregate.Phase == delegatestore.PhaseStopping {
			return true
		}
	}
	return false
}

func (c *delegateTreeController) stableDelegateOwnedBySessionLocked(owner *Session, aggregate *delegatestore.Aggregate) bool {
	if owner == nil || aggregate == nil || owner.delegateController != c {
		return false
	}
	parentID := aggregate.Descriptor.ParentDelegateID
	if owner.owningDelegateID == "" {
		return parentID == "" && owner.id == c.rootSessionID && aggregate.Descriptor.OwnerSessionID == c.rootSessionID
	}
	return parentID == owner.owningDelegateID
}

// closeStableWorktreeResumability atomically revalidates that a direct-owned
// subtree is quiescent and fsyncs its permanent worktree-disposal closure.
// allowControllerClose is used only by the already-fenced root close path.
func (c *delegateTreeController) closeStableWorktreeResumability(owner *Session, delegateID, reason string, allowControllerClose bool) (stableDelegateWorktreeSnapshot, bool, delegateMutationPlans, error) {
	if c == nil || owner == nil {
		return stableDelegateWorktreeSnapshot{}, false, delegateMutationPlans{}, errDelegateNotControllable
	}
	c.mu.Lock()
	for {
		if c.closing && !allowControllerClose {
			c.mu.Unlock()
			return stableDelegateWorktreeSnapshot{}, false, delegateMutationPlans{}, errDelegateTargetBusy
		}
		if blocker := c.attentionStartBlockerLocked(delegateID, true); blocker != nil {
			c.mu.Unlock()
			<-blocker
			c.mu.Lock()
			continue
		}
		break
	}
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil || !c.stableDelegateOwnedBySessionLocked(owner, aggregate) {
		return stableDelegateWorktreeSnapshot{}, false, delegateMutationPlans{}, errDelegateNotControllable
	}
	if aggregate.Descriptor.Isolation != "worktree" {
		return stableDelegateWorktreeSnapshot{}, false, delegateMutationPlans{}, fmt.Errorf("delegate %s is not worktree-isolated", delegateID)
	}
	members := c.subtreeMembersLocked(delegateID)
	if c.stableWorktreeSubtreeActiveLocked(members) || c.runtimeReclamationIntersectsProcessWorkLocked(members) {
		return stableDelegateWorktreeSnapshot{}, false, delegateMutationPlans{}, errDelegateTargetBusy
	}
	if strings.TrimSpace(reason) == "" {
		return stableDelegateWorktreeSnapshot{}, false, delegateMutationPlans{}, errDelegateTargetBusy
	}
	if !aggregate.Resumable {
		return c.stableWorktreeSnapshotLocked(delegateID, aggregate), true, delegateMutationPlans{}, nil
	}
	plan, err := c.appendResumabilityClosureLocked(delegateID, delegatestore.Event{
		Kind:       delegatestore.EventDelegateResumabilityClosed,
		DelegateID: delegateID,
		ResumabilityClosed: &delegatestore.ResumabilityClosed{
			Reason: reason,
		},
	})
	if err != nil {
		return stableDelegateWorktreeSnapshot{}, false, delegateMutationPlans{}, err
	}
	row := c.stableWorktreeSnapshotLocked(delegateID, c.durable[delegateID])
	return row, false, delegateMutationPlans{updates: []delegateUpdatePlan{plan}}, nil
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
	if c.reclamationCoversLocked(lease.delegateID) {
		return nil, nil, errDelegateTargetBusy
	}
	aggregate, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return nil, nil, err
	}
	if live.recoveryRequired || aggregate.PendingStopSeq != 0 || !aggregate.Resumable || live.binding == nil || !live.binding.ready {
		return nil, nil, errDelegateTargetBusy
	}
	if blocked, _ := c.ancestorFenceLocked(aggregate.Descriptor.ParentDelegateID); blocked {
		return nil, nil, errDelegateTargetBusy
	}
	if slices.Contains(phases, aggregate.Phase) {
		return aggregate, live, nil
	}
	return nil, nil, errDelegateTargetBusy
}

// ancestorFenceLocked walks the ancestor chain starting at parentID and
// reports whether any ancestor refuses work under it -- the same policy
// admitLeaseLocked applies to running leases. closedAncestorID identifies the
// nearest ancestor whose refusal is permanent: resumability closes
// monotonically (no event reopens it), so a missing or non-resumable ancestor
// can never admit this subtree again. PhaseClosed needs no separate check
// because every fold transition into it requires !Resumable.
//
// A blocked result with an empty closedAncestorID is transient -- a pending
// subtree stop that clears when the stop completes. The controller's own APIs
// never leave a delegate outside the stop that covers its ancestor (the stop
// request marks whole subtrees and creation under a stop is refused), so
// callers that already checked the delegate's own PendingStopSeq reach this
// branch only through a journal the fold accepts but this controller did not
// emit; parking, not escalating, is the safe answer for such a journal.
func (c *delegateTreeController) ancestorFenceLocked(parentID string) (blocked bool, closedAncestorID string) {
	for ancestorID := parentID; ancestorID != ""; {
		ancestor := c.durable[ancestorID]
		if ancestor == nil || !ancestor.Resumable {
			return true, ancestorID
		}
		if ancestor.PendingStopSeq != 0 {
			return true, ""
		}
		ancestorID = ancestor.Descriptor.ParentDelegateID
	}
	return false, ""
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

// blockingDelegateIDs returns this session's direct child delegates whose
// current run has a live inline waiter. The controller owns both pieces of
// state, so this is the authoritative dependency check: a running delegate
// without a waiter is background work, while a waiter that outlived its
// current run is stale.
func (c *delegateTreeController) blockingDelegateIDs(rootSessionID, parentDelegateID string) []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if rootSessionID == "" {
		rootSessionID = c.rootSessionID
	}
	ids := make([]string, 0)
	for id, aggregate := range c.durable {
		if aggregate == nil || aggregate.Descriptor.OwnerSessionID != rootSessionID || aggregate.Descriptor.ParentDelegateID != parentDelegateID || !aggregate.CurrentRunOpen {
			continue
		}
		live := c.live[id]
		if live == nil || len(live.waiters) == 0 {
			continue
		}
		for generation, waiter := range live.waiters {
			if waiter != nil && generation == aggregate.Generation && waiter.generation == aggregate.Generation {
				ids = append(ids, id)
				break
			}
		}
	}
	sort.Strings(ids)
	return ids
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

func (c *delegateTreeController) appendResumabilityClosureLocked(delegateID string, events ...delegatestore.Event) (delegateUpdatePlan, error) {
	members := c.subtreeMembersLocked(delegateID)
	revisions := make(map[string]uint64, len(members))
	for id := range members {
		revisions[id] = c.durable[id].ProjectionRevision
	}
	if _, err := c.appendLocked(events...); err != nil {
		return delegateUpdatePlan{}, err
	}
	ids := make([]string, 0, len(members))
	for id, revision := range revisions {
		if c.durable[id].ProjectionRevision != revision {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	rows := make([]delegateSnapshot, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, c.captureDelegateSnapshotLocked(id))
	}
	return delegateUpdatePlan{rows: rows}, nil
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
		if aggregate.LatestOutcome.Resumable != nil {
			resumable := *aggregate.LatestOutcome.Resumable
			value.Resumable = &resumable
		}
		outcome = &value
	}
	return delegateSnapshot{
		id:                 aggregate.DelegateID,
		parentID:           aggregate.Descriptor.ParentDelegateID,
		descriptor:         cloneDelegateStartDescriptor(aggregate.Descriptor),
		generation:         aggregate.Generation,
		lifecycle:          lifecycle,
		phase:              aggregate.Phase,
		currentRunOpen:     aggregate.CurrentRunOpen,
		runStartedAt:       aggregate.RunStartedAt,
		resumable:          aggregate.Resumable,
		needsAttention:     aggregate.NeedsAttention,
		revision:           aggregate.ProjectionRevision,
		transcriptRef:      aggregate.Descriptor.TranscriptRef,
		notResumableReason: aggregate.NotResumableReason,
		latestActivityAt:   aggregate.LatestActivityAt,
		pendingStopSeq:     aggregate.PendingStopSeq,
		pendingStopAt:      aggregate.PendingStopAt,
		lastOutcome:        outcome,
		latestPacket:       cloneStableTerminalPacket(aggregate.LatestPacket),
	}
}

func cloneStableTerminalPacket(packet *delegatestore.TerminalPacket) *delegatestore.TerminalPacket {
	if packet == nil {
		return nil
	}
	clone := *packet
	clone.Message = append(json.RawMessage(nil), packet.Message...)
	clone.StructuredResult = append(json.RawMessage(nil), packet.StructuredResult...)
	clone.Warnings = append([]string(nil), packet.Warnings...)
	clone.Metadata = append(json.RawMessage(nil), packet.Metadata...)
	if packet.StructuredResultValid != nil {
		valid := *packet.StructuredResultValid
		clone.StructuredResultValid = &valid
	}
	return &clone
}

func (c *delegateTreeController) capacityInUse() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turnsInUse, c.drivesInUse
}
