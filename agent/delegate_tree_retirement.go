package agent

import (
	"errors"
	"os"
	"slices"
	"sort"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
)

// The root runtime is immutable for the lifetime of a delegate controller.
// Its atomic process pointer may be published later by AttachRoot.
func (c *delegateTreeController) beginRetirementMutation() (func(), error) {
	if c != nil && c.rootRuntime != nil {
		return c.rootRuntime.beginRetirementMutation("delegate")
	}
	return func() {}, nil
}

func (c *delegateTreeController) retirementChanged() {
	if c != nil && c.rootRuntime != nil {
		if outer := c.rootRuntime.retirementController.Load(); outer != nil {
			outer.Changed()
		}
	}
}

// setRetirementFence is called only after the outer controller has closed
// admission. The exact claim, not a durable phase, owns this process fence.
func (c *delegateTreeController) setRetirementFence(claim *RetirementClaim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retirementClaim != nil && c.retirementClaim != claim {
		return ErrRetirementUnavailable
	}
	c.retirementClaim = claim
	return nil
}

func (c *delegateTreeController) clearRetirementFence(claim *RetirementClaim) {
	c.mu.Lock()
	if c.retirementClaim == claim {
		c.retirementClaim = nil
	}
	c.mu.Unlock()
}

// retirementEvidence scans the shared owner once, including durable-only
// members. Session and filesystem evidence is deliberately read after mu drops.
func (c *delegateTreeController) retirementEvidence() ([]RetirementBlocker, []*Session, error) {
	if c == nil {
		return nil, nil, errors.New("delegate controller is unavailable")
	}
	c.mu.Lock()
	version, fence := c.evidenceVersion, c.retirementClaim
	members := make(map[string]struct{}, len(c.durable))
	for id := range c.durable {
		members[id] = struct{}{}
	}
	blockers, sessions, exact, descriptors, ids := c.retirementOwnerScanLocked(members)
	c.mu.Unlock()
	for _, id := range ids {
		d, ok := descriptors[id]
		if !ok {
			continue
		}
		if d.ChildSessionID == "" || d.TranscriptRef == "" || d.OwnerSessionID != c.rootSessionID {
			blockers = append(blockers, RetirementBlocker{Category: "delegate", SessionID: c.rootSessionID, DelegateID: id})
			continue
		}
		path, sessionID, refErr := delegateTranscriptPathFromRef(c.stateDir, d.TranscriptRef)
		if refErr != nil || sessionID != d.ChildSessionID {
			blockers = append(blockers, RetirementBlocker{Category: "delegate", SessionID: c.rootSessionID, DelegateID: id})
			continue
		}
		if exact[id] == nil {
			meta, err := schema.LoadSessionMeta(c.stateDir, d.ChildSessionID)
			info, statErr := os.Stat(path)
			if err != nil || meta.ID != d.ChildSessionID || statErr != nil || !info.Mode().IsRegular() {
				blockers = append(blockers, RetirementBlocker{Category: "delegate", SessionID: c.rootSessionID, DelegateID: id})
				continue
			}
			if meta.Goal != nil && meta.Goal.Status == "active" {
				blockers = append(blockers, RetirementBlocker{Category: "autonomous", SessionID: d.ChildSessionID, DelegateID: id})
			}
			fold, err := readExistingDelegateAttentionFold(path, sessionID)
			if err != nil || len(fold.pendingIDs()) != 0 {
				blockers = append(blockers, RetirementBlocker{Category: "delegate", SessionID: c.rootSessionID, DelegateID: id})
			}
			local, err := retirementColdEvidence(c.stateDir, d.ChildSessionID, id)
			blockers = append(blockers, local...)
			if err != nil {
				return blockers, nil, err
			}
		}
	}
	for _, s := range sessions {
		local, err := s.retirementEvidence()
		blockers = append(blockers, local...)
		if err != nil {
			return blockers, nil, err
		}
	}
	if c.rootRuntime != nil {
		local, err := c.rootRuntime.retirementEvidence()
		blockers = append(blockers, local...)
		if err != nil {
			return blockers, nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.evidenceVersion != version || c.retirementClaim != fence {
		return blockers, nil, errDelegateTargetBusy
	}
	for id, s := range exact {
		if live := c.live[id]; live == nil || live.runtime != s {
			return blockers, nil, errDelegateTargetBusy
		}
	}
	return blockers, sessions, nil
}

// retirementOwnerScanLocked performs the in-memory owner scan shared by the full
// and non-blocking reads. The caller must hold c.mu; it does no filesystem or
// per-Session work. It returns the owner blockers, the live sessions for members
// with a resident runtime, the exact runtime pointer per member (the full read
// revalidates these after dropping mu), the durable descriptors, and the sorted
// member ids.
func (c *delegateTreeController) retirementOwnerScanLocked(members map[string]struct{}) (
	blockers []RetirementBlocker,
	sessions []*Session,
	exact map[string]*Session,
	descriptors map[string]delegatestore.Descriptor,
	ids []string,
) {
	block := func(id string) {
		blockers = append(blockers, RetirementBlocker{Category: "delegate", SessionID: c.rootSessionID, DelegateID: id})
	}
	// Root-owned and not-yet-durable registrations must also block an empty tree.
	if c.closing || c.stop != nil || len(c.reclamations) != 0 || len(c.reclaiming) != 0 || len(c.reservations) != 0 || len(c.inputClaims) != 0 || len(c.steeringClaims) != 0 || len(c.modelClaims) != 0 || len(c.settlementClaims) != 0 || len(c.work) != 0 || len(c.deliveries) != 0 || len(c.deliveryClaims) != 0 || len(c.quietClaims) != 0 || len(c.watchEnqueues) != 0 || len(c.watchDeliveries) != 0 || len(c.reconcileOrder) != 0 || c.owedAdmission || c.turnsInUse != 0 || c.drivesInUse != 0 || c.runtimeReclamationIntersectsProcessWorkLocked(members) {
		block("")
	}
	if c.stopDriver != nil {
		select {
		case <-c.stopDriver.done:
		default:
			block("")
		}
	}
	ids = make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Validate ancestry before using postorder; corrupt cycles must not loop.
	ordered := make([]string, 0, len(ids))
	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	var visit func(string)
	visit = func(id string) {
		if visiting[id] {
			block(id)
			return
		}
		if visited[id] {
			return
		}
		visiting[id] = true
		if a := c.durable[id]; a != nil && a.Descriptor.ParentDelegateID != "" {
			if _, ok := members[a.Descriptor.ParentDelegateID]; !ok {
				block(id)
			} else {
				visit(a.Descriptor.ParentDelegateID)
			}
		}
		visiting[id] = false
		visited[id] = true
		ordered = append(ordered, id)
	}
	for _, id := range ids {
		visit(id)
	}
	exact = make(map[string]*Session)
	descriptors = make(map[string]delegatestore.Descriptor)
	for _, id := range slices.Backward(ordered) {
		a := c.durable[id]
		if a == nil {
			block(id)
			continue
		}
		descriptors[id] = a.Descriptor
		if a.CurrentRunOpen || a.Phase != delegatestore.PhaseIdle && a.Phase != delegatestore.PhaseClosed || len(a.PendingDeliveries) != 0 || a.PendingStopSeq != 0 || a.NeedsAttention || a.PreparedTerminal != nil {
			block(id)
		}
		if len(c.attentionWakeIDs[id]) != 0 {
			block(id)
		}
		if live := c.live[id]; live != nil {
			if live.binding != nil || live.recoveryRequired || live.finalizationRecoveryRequired || live.recoveryRunnerPending || liveGenerationOwesSteering(live) || len(live.waiters) != 0 || live.quietClaim != nil || len(live.attentionIDs) != 0 {
				block(id)
			}
			if live.runtime != nil {
				exact[id] = live.runtime
				sessions = append(sessions, live.runtime)
			}
		}
	}
	for id := range c.live {
		if _, ok := members[id]; !ok {
			block(id)
		}
	}
	return blockers, sessions, exact, descriptors, ids
}

// retirementNonBlockingEvidence reads the shared tree's in-memory owner state and
// every live member's non-blocking Session obligations. It deliberately skips the
// durable-only member validation the full read performs after dropping mu:
// schema.LoadSessionMeta, os.Stat, readExistingDelegateAttentionFold and
// retirementColdEvidence (including its background-context jobs.jsonl scan) all do
// unbounded filesystem I/O, which an attempt may not wait on while work is
// admitted. The in-memory owner state is kept because it carries obligations that
// are not lease-backed (reconciliation, stop, owed admission, in-use turns and
// drives, claimed deliveries); the full read re-checks the cold members once
// nothing is admitted, which is the only time the complete set is required.
//
// The owner scan is taken with TryLock, not Lock: c.mu is itself held across a
// durable delegate-store append (appendResumabilityClosureLocked ->
// appendLocked -> store.AppendBatch, delegate_tree_controller.go:830,303,309),
// so blocking on it would wait on admitted work. When the owner is mid-append the
// in-memory owner state cannot be read, and it carries obligations that are not
// lease-backed, so returning nothing would silently under-report a possible
// blocker. This fails closed with a placeholder rather than asserting the tree is
// clear.
func (c *delegateTreeController) retirementNonBlockingEvidence() []RetirementBlocker {
	if c == nil {
		return nil
	}
	if !c.mu.TryLock() {
		// The placeholder is deliberately shape-honest: an empty DelegateID names
		// no specific delegate, because none was read. It states that root-owned
		// tree state exists and could not be inspected, not that a particular
		// delegate is blocked. It never appears on a decision path — the retire
		// and refuse decision uses retirementEvidence, which blocks for c.mu — so
		// it can only add a caution to an already-refusing diagnostic snapshot.
		return []RetirementBlocker{{Category: "delegate", SessionID: c.rootSessionID}}
	}
	members := make(map[string]struct{}, len(c.durable))
	for id := range c.durable {
		members[id] = struct{}{}
	}
	blockers, sessions, _, _, _ := c.retirementOwnerScanLocked(members)
	c.mu.Unlock()
	for _, s := range sessions {
		blockers = append(blockers, s.retirementNonBlockingEvidence()...)
	}
	if c.rootRuntime != nil {
		blockers = append(blockers, c.rootRuntime.retirementNonBlockingEvidence()...)
	}
	return blockers
}

// releaseDelegateRuntimePointer clears the controller's process-local runtime
// pointer for one delegate whose resident runtime was just released outside the
// retirement flow (issue #481 unlock). Without it a released *Session stays as
// the delegate's live runtime and a later delegate_send's fresh restore collides
// with it in AttachRuntime. It changes only the exact pointer after its owner
// reports release; it never closes resumability or appends durable events.
func (c *delegateTreeController) releaseDelegateRuntimePointer(delegateID string, released *Session) {
	if c == nil || delegateID == "" || released == nil {
		return
	}
	c.mu.Lock()
	if live := c.live[delegateID]; live != nil && live.binding == nil && live.runtime == released {
		live.runtime = nil
		c.evidenceVersion++
	}
	c.mu.Unlock()
}

// releaseRetiredRuntimes changes only exact process pointers after their owner
// reports release. It never closes resumability or appends durable events.
func (c *delegateTreeController) releaseRetiredRuntimes(exact map[string]*Session) error {
	defer c.retirementChanged()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retirementClaim == nil {
		return ErrRetirementUnavailable
	}
	for id, s := range exact {
		if live := c.live[id]; s != nil && live != nil && live.binding == nil && live.runtime == s {
			live.runtime = nil
		}
	}
	c.evidenceVersion++
	return nil
}
