package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

type delegateRuntimeReclamationEntry struct {
	delegateID     string
	childSessionID string
	runtime        *Session
	ownerRuntime   *Session
}

type delegateRuntimeReclamationClaim struct {
	token   uint64
	roots   []delegateRuntimeReclamationEntry
	entries []delegateRuntimeReclamationEntry
}

type delegateRuntimeReclamationCandidate struct {
	root         delegateRuntimeReclamationEntry
	entries      []delegateRuntimeReclamationEntry
	closed       bool
	acknowledged bool
	endedAt      time.Time
}

// ClaimRuntimeReclamation reserves enough quiescent terminal runtime subtrees
// to admit required new resident runtimes. The durable delegate tree is not
// mutated; the claim only fences process-local runtime ownership while callers
// close the selected sessions outside the controller mutex.
func (c *delegateTreeController) ClaimRuntimeReclamation(required int) (*delegateRuntimeReclamationClaim, error) {
	retirementRelease, retirementErr := c.beginRetirementMutation()
	if retirementErr != nil {
		return nil, retirementErr
	}
	defer retirementRelease()
	if c == nil {
		return nil, errors.New("delegate controller is unavailable")
	}
	if required <= 0 {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, errDelegateTargetBusy
	}

	resident := 0
	// A runtime subtree another in-flight claim — an idle release closing a
	// just-finalized generation — is already settling frees capacity on its
	// failing loudly for capacity that is about to appear. An aborted claim
	// restores residency and a later spawn recomputes, so an over-promise
	// self-heals rather than stranding the spawn.
	inFlight := 0
	for id, aggregate := range c.durable {
		if c.isResidentTerminalRuntimeLocked(id, aggregate) {
			resident++
			if _, covered := c.reclaiming[id]; covered {
				inFlight++
			}
		}
	}
	needed := resident + required - c.maxRetainedTerminal - inFlight
	if needed <= 0 {
		return nil, nil
	}

	candidatesByID := make(map[string]delegateRuntimeReclamationCandidate)
	for id, aggregate := range c.durable {
		if !c.isResidentTerminalRuntimeLocked(id, aggregate) {
			continue
		}
		entries, ok := c.claimableRuntimeSubtreeLocked(id)
		if !ok {
			continue
		}
		candidatesByID[id] = delegateRuntimeReclamationCandidate{
			root:         c.runtimeReclamationEntryLocked(id),
			entries:      entries,
			closed:       aggregate.Phase == delegatestore.PhaseClosed || !aggregate.Resumable,
			acknowledged: len(aggregate.PendingDeliveries) == 0,
			endedAt:      aggregate.LatestOutcome.EndedAt,
		}
	}

	// A claim closes a whole quiescent subtree. Keep only its highest resident
	// root so descendants are never selected twice or closed independently of a
	// reclaimable retained parent.
	candidates := make([]delegateRuntimeReclamationCandidate, 0, len(candidatesByID))
	for id, candidate := range candidatesByID {
		hasCandidateAncestor := false
		for parentID := c.durable[id].Descriptor.ParentDelegateID; parentID != ""; {
			if _, ok := candidatesByID[parentID]; ok {
				hasCandidateAncestor = true
				break
			}
			parent := c.durable[parentID]
			if parent == nil {
				break
			}
			parentID = parent.Descriptor.ParentDelegateID
		}
		if !hasCandidateAncestor {
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.closed != right.closed {
			return left.closed
		}
		if left.acknowledged != right.acknowledged {
			return left.acknowledged
		}
		if !left.endedAt.Equal(right.endedAt) {
			return left.endedAt.Before(right.endedAt)
		}
		return left.root.delegateID < right.root.delegateID
	})

	selected := make([]delegateRuntimeReclamationCandidate, 0, len(candidates))
	claimed := 0
	for _, candidate := range candidates {
		selected = append(selected, candidate)
		claimed += len(candidate.entries)
		if claimed >= needed {
			break
		}
	}
	if claimed < needed {
		return nil, fmt.Errorf("retained delegate limit reached (%d): no quiescent terminal runtime subtree can reclaim %d required slot(s)", c.maxRetainedTerminal, needed)
	}

	c.nextToken++
	claim := &delegateRuntimeReclamationClaim{token: c.nextToken}
	for _, candidate := range selected {
		claim.roots = append(claim.roots, candidate.root)
		claim.entries = append(claim.entries, candidate.entries...)
	}
	for _, entry := range claim.entries {
		c.reclaiming[entry.delegateID] = claim.token
	}
	c.reclamations[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

// CompleteRuntimeReclamation clears only the exact runtime pointers that the
// caller reports closed. A replacement installed after the claim survives.
func (c *delegateTreeController) CompleteRuntimeReclamation(claim *delegateRuntimeReclamationClaim, closed map[string]*Session) error {
	defer c.retirementChanged()
	if c == nil {
		return errors.New("delegate controller is unavailable")
	}
	if claim == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reclamations[claim.token] != claim {
		return errDelegateStaleLease
	}
	for _, entry := range claim.entries {
		if closed[entry.delegateID] != entry.runtime {
			continue
		}
		if live := c.live[entry.delegateID]; live != nil && live.binding == nil && live.runtime == entry.runtime {
			live.runtime = nil
		}
	}
	c.releaseRuntimeReclamationLocked(claim)
	return nil
}

func (c *delegateTreeController) AbortRuntimeReclamation(claim *delegateRuntimeReclamationClaim) error {
	defer c.retirementChanged()
	if c == nil {
		return errors.New("delegate controller is unavailable")
	}
	if claim == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reclamations[claim.token] != claim {
		return errDelegateStaleLease
	}
	c.releaseRuntimeReclamationLocked(claim)
	return nil
}

func (c *delegateTreeController) releaseRuntimeReclamationLocked(claim *delegateRuntimeReclamationClaim) {
	delete(c.reclamations, claim.token)
	for _, entry := range claim.entries {
		if c.reclaiming[entry.delegateID] == claim.token {
			delete(c.reclaiming, entry.delegateID)
		}
	}
	c.evidenceVersion++
}

func (c *delegateTreeController) isResidentTerminalRuntimeLocked(id string, aggregate *delegatestore.Aggregate) bool {
	if aggregate == nil || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.Phase != delegatestore.PhaseIdle && aggregate.Phase != delegatestore.PhaseClosed {
		return false
	}
	live := c.live[id]
	return live != nil && live.binding == nil && live.runtime != nil
}

func (c *delegateTreeController) claimableRuntimeSubtreeLocked(rootID string) ([]delegateRuntimeReclamationEntry, bool) {
	members := c.subtreeMembersLocked(rootID)
	if c.runtimeReclamationIntersectsProcessWorkLocked(members) {
		return nil, false
	}
	entries := make([]delegateRuntimeReclamationEntry, 0, len(members))
	for _, id := range c.memberIDsLeafFirstLocked(members) {
		aggregate := c.durable[id]
		if aggregate == nil || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.Phase != delegatestore.PhaseIdle && aggregate.Phase != delegatestore.PhaseClosed {
			return nil, false
		}
		if live := c.live[id]; live != nil {
			// Only steering a LIVE generation still owes counts as work in
			// flight. An admission carried across a covering stop is a parcel
			// for a successor, and stopping a delegate is normally terminal --
			// once resumability is closed ReserveStart refuses, so no successor
			// can ever consume it. Counting it here would pin this runtime, and
			// the bail below is subtree-wide, so it would pin every sibling too:
			// one stopped-and-forgotten delegate burning a maxRetainedTerminal
			// slot for the life of the process.
			if live.binding != nil || live.recoveryRequired || live.finalizationRecoveryRequired || live.recoveryRunnerPending || liveGenerationOwesSteering(live) || len(live.waiters) != 0 || live.quietClaim != nil {
				return nil, false
			}
			if live.runtime != nil {
				entries = append(entries, c.runtimeReclamationEntryLocked(id))
			}
		}
	}
	return entries, len(entries) != 0
}

func (c *delegateTreeController) runtimeReclamationEntryLocked(id string) delegateRuntimeReclamationEntry {
	aggregate := c.durable[id]
	live := c.live[id]
	entry := delegateRuntimeReclamationEntry{delegateID: id}
	if aggregate != nil {
		entry.childSessionID = aggregate.Descriptor.ChildSessionID
		entry.ownerRuntime = c.ownerRuntimeLocked(aggregate)
	}
	if live != nil {
		entry.runtime = live.runtime
	}
	return entry
}

func (c *delegateTreeController) runtimeReclamationIntersectsProcessWorkLocked(members map[string]struct{}) bool {
	if c.stop != nil {
		for id := range members {
			if _, ok := c.stop.members[id]; ok {
				return true
			}
		}
	}
	for id := range members {
		if _, ok := c.reclaiming[id]; ok {
			return true
		}
	}
	for _, reservation := range c.reservations {
		if reservation != nil && delegateIDInSet(reservation.delegateID, members) {
			return true
		}
	}
	for _, lease := range c.inputClaims {
		if delegateIDInSet(lease.delegateID, members) {
			return true
		}
	}
	for _, claim := range c.steeringClaims {
		if claim != nil && delegateIDInSet(claim.delegateID, members) {
			return true
		}
	}
	for _, claim := range c.modelClaims {
		if claim != nil && delegateIDInSet(claim.lease.delegateID, members) {
			return true
		}
	}
	for _, claim := range c.settlementClaims {
		if claim != nil && delegateIDInSet(claim.lease.delegateID, members) {
			return true
		}
	}
	for _, work := range c.work {
		if work != nil && delegateIDInSet(work.owner.delegateID, members) {
			return true
		}
	}
	for _, receipt := range c.deliveries {
		if receipt != nil && (delegateIDInSet(receipt.delegateID, members) || delegateIDInSet(receipt.ownerID, members)) {
			return true
		}
	}
	for _, claim := range c.deliveryClaims {
		if claim != nil && (delegateIDInSet(claim.delegateID, members) || delegateIDInSet(claim.ownerID, members)) {
			return true
		}
	}
	for _, claim := range c.quietClaims {
		if claim != nil && delegateIDInSet(claim.lease.delegateID, members) {
			return true
		}
	}
	for _, receipt := range c.watchEnqueues {
		if receipt != nil && (delegateIDInSet(receipt.sourceDelegateID, members) || delegateIDInSet(receipt.receiverDelegateID, members)) {
			return true
		}
	}
	for _, receipt := range c.watchDeliveries {
		if receipt != nil && (delegateIDInSet(receipt.sourceDelegateID, members) || delegateIDInSet(receipt.receiverDelegateID, members)) {
			return true
		}
	}
	for _, lease := range c.reconcileOrder {
		if delegateIDInSet(lease.delegateID, members) {
			return true
		}
	}
	return false
}

func delegateIDInSet(id string, ids map[string]struct{}) bool {
	if id == "" {
		return false
	}
	_, ok := ids[id]
	return ok
}

func (c *delegateTreeController) reclamationCoversLocked(delegateID string) bool {
	for id := delegateID; id != ""; {
		if _, ok := c.reclaiming[id]; ok {
			return true
		}
		aggregate := c.durable[id]
		if aggregate == nil {
			return false
		}
		id = aggregate.Descriptor.ParentDelegateID
	}
	return false
}

// ownerRuntimeLocked returns the resident session that tracks the delegate's
// runtime as a child: the root runtime for a root delegate, otherwise the
// parent delegate's runtime, or nil when the parent is not resident.
func (c *delegateTreeController) ownerRuntimeLocked(aggregate *delegatestore.Aggregate) *Session {
	parentID := aggregate.Descriptor.ParentDelegateID
	if parentID == "" {
		return c.rootRuntime
	}
	if parent := c.live[parentID]; parent != nil {
		return parent.runtime
	}
	return nil
}

func (s *Session) reclaimDelegateRuntimeCapacity(required int) (err error) {
	if s == nil || s.delegateController == nil {
		return errors.New("delegate controller is unavailable")
	}
	claim, err := s.delegateController.ClaimRuntimeReclamation(required)
	if err != nil || claim == nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			err = errors.Join(err, s.delegateController.AbortRuntimeReclamation(claim))
		}
	}()

	closed := make(map[string]*Session, len(claim.entries))
	for _, entry := range claim.entries {
		if entry.runtime == nil {
			continue
		}
		if entry.ownerRuntime != nil && entry.ownerRuntime.subagents != nil {
			entry.ownerRuntime.subagents.removeSession(entry.childSessionID, entry.runtime)
		}
		if closeRuntime := s.cfg.testOnly.delegateRuntimeReclaimClose; closeRuntime != nil {
			closeRuntime(entry.runtime)
		} else {
			teardownChildSession(context.Background(), entry.runtime, retainChildScratch)
		}
		closed[entry.delegateID] = entry.runtime
	}
	if err := s.delegateController.CompleteRuntimeReclamation(claim, closed); err != nil {
		return err
	}
	completed = true
	return nil
}

// ClaimIdleRuntimeRelease reserves one named quiescent terminal delegate's
// resident runtime subtree for non-terminal idle release: the caller unhooks
// and releases those runtimes outside the controller mutex and reports them
// closed via CompleteRuntimeReclamation, which clears the live runtime
// pointers so future sends and drives take the cold restore path. It is the
// targeted counterpart of ClaimRuntimeReclamation, which selects whole
// subtrees by retained-terminal capacity pressure; the idle release after a
// generation finalizes wants exactly one named subtree.
//
// A nil claim with a nil error means "not releasable right now": the delegate
// is missing, not terminal-idle, holds no resident runtime, or its subtree
// intersects process work — pending deliveries, claims, watchers, recovery
// flags, waiters, or a shared task store owned inside the subtree — that must
// settle first. Callers skip without forcing; the delegate's next finalize or
// the capacity reclamation backstop will retry.
func (c *delegateTreeController) ClaimIdleRuntimeRelease(delegateID string) (*delegateRuntimeReclamationClaim, error) {
	retirementRelease, retirementErr := c.beginRetirementMutation()
	if retirementErr != nil {
		return nil, retirementErr
	}
	defer retirementRelease()
	if c == nil {
		return nil, errors.New("delegate controller is unavailable")
	}
	if delegateID == "" {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, errDelegateTargetBusy
	}
	aggregate := c.durable[delegateID]
	if aggregate == nil || !c.isResidentTerminalRuntimeLocked(delegateID, aggregate) {
		return nil, nil
	}
	entries, ok := c.claimableRuntimeSubtreeLocked(delegateID)
	if !ok {
		return nil, nil
	}
	if c.subtreeOwnsSharedTaskStoreLocked(entries) {
		return nil, nil
	}
	c.nextToken++
	claim := &delegateRuntimeReclamationClaim{
		token:   c.nextToken,
		roots:   []delegateRuntimeReclamationEntry{c.runtimeReclamationEntryLocked(delegateID)},
		entries: entries,
	}
	for _, entry := range claim.entries {
		c.reclaiming[entry.delegateID] = claim.token
	}
	c.reclamations[claim.token] = claim
	c.evidenceVersion++
	return claim, nil
}

// subtreeOwnsSharedTaskStoreLocked reports whether releasing the claim's
// subtree would strand a shared task store resolver: some aggregate — inside
// the subtree or anywhere else in the tree — names one of the subtree's
// sessions as its shared task store owner. A released owner has no resident
// runtime, so resolveStableSharedTaskStore's owner-residency requirement would
// fail the resolver's next resume.
func (c *delegateTreeController) subtreeOwnsSharedTaskStoreLocked(entries []delegateRuntimeReclamationEntry) bool {
	owned := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		owned[entry.childSessionID] = struct{}{}
	}
	for _, aggregate := range c.durable {
		if owner := aggregate.Descriptor.SharedTaskStoreOwnerSessionID; owner != "" {
			if _, ok := owned[owner]; ok {
				return true
			}
		}
	}
	return false
}

// releaseIdleRuntimeAfterFinalize non-terminally releases this just-finalized
// stable delegate's resident runtime subtree. It unhooks each member's record
// from its owner's subagent manager, releases every member's runtime under
// the retirement policy — settling process-local resources, stdio MCP server
// processes above all, while durable identity, transcripts, scratch pins, and
// resumability stay intact — and clears the controller's live runtime
// pointers so the next send or drive takes the cold restore path.
//
// The caller is the finished child's own run goroutine at the end of the
// finalize tail: the generation outcome is durably committed, remaining
// delegate attention is re-armed, and quiescence is reported. Every quiescence
// precondition is checked BEFORE any teardown runs, because releaseRuntime
// consumes the session's single teardown pass: a refusal arriving mid-release
// would leave a half-settled runtime that can never be warm-resumed.
//
// It returns false — leaving the runtime warm — when any gate fails, and
// never forces: a refused delegate retries at its next finalize or through
// the capacity reclamation backstop. A root session (no owning delegate
// identity) hosts the tree and must stay resident; a non-stable child has no
// cold-restore path, so it is out of scope by construction (its parent is a
// stable delegate, and releasing that parent drains it as a subtree member).
func (s *Session) releaseIdleRuntimeAfterFinalize() bool {
	if s == nil || s.delegateController == nil || s.owningDelegateID == "" {
		return false
	}
	claim, err := s.delegateController.ClaimIdleRuntimeRelease(s.owningDelegateID)
	if err != nil {
		s.emit(events.EventWarning, warningDataFromError("idle runtime release claim failed", err))
		return false
	}
	if claim == nil {
		return false
	}
	completed := false
	defer func() {
		// Abort only while the claim can still be meaningful: once the pointers
		// are cleared the claim is released and Abort would just report a stale
		// lease; every path below either completes the claim or fails before
		// any controller mutation, so the abort path never leaves a half-done
		// release.
		if !completed {
			_ = s.delegateController.AbortRuntimeReclamation(claim)
		}
	}()

	// Gate EVERY member before unhooking or tearing ANY: these are the
	// conditions releaseQuiescentRuntime refuses on mid-release (running jobs,
	// pending terminal flush) plus the notification and watch-send residue a
	// released runtime could strand.
	for _, entry := range claim.entries {
		if entry.runtime == nil {
			continue
		}
		if !entry.runtime.idleReleasePregatesClear() {
			return false
		}
	}

	// Unhook first — record removal plus live-pointer clear — and only then run
	// the teardowns, WITHOUT holding the reclamation fence across them. The
	// fence exists to keep process work off a runtime that is being DESTROYED;
	// this release is non-terminal, so a send racing the teardown must find a
	// non-resident idle delegate and cold-restore a fresh runtime, exactly as
	// it would after a daemon restart, instead of being refused target_busy by
	// the fence. CompleteRuntimeReclamation clears only the exact pointers this
	// claim captured, so a replacement runtime installed by such a racing send
	// survives.
	closed := make(map[string]*Session, len(claim.entries))
	for _, entry := range claim.entries {
		if entry.runtime != nil {
			closed[entry.delegateID] = entry.runtime
		}
		if entry.ownerRuntime != nil && entry.ownerRuntime.subagents != nil {
			entry.ownerRuntime.subagents.removeSession(entry.childSessionID, entry.runtime)
		}
	}
	if err := s.delegateController.CompleteRuntimeReclamation(claim, closed); err != nil {
		s.emit(events.EventWarning, warningDataFromError("idle runtime release completion failed", err))
		return false
	}
	completed = true

	for _, entry := range claim.entries {
		if entry.runtime == nil {
			continue
		}
		// A teardown error after the pre-gates leaves the pass spent — this
		// runtime instance can never be warm-resumed — so unhooking is the
		// consistent outcome either way: durable state and scratch pins survive
		// for a cold restore, and the teardown body emits its own warnings for
		// the settlements that did not complete, the same contract
		// reclaimDelegateRuntimeCapacity's teardowns already follow.
		_ = teardownChildSessionWithPolicy(context.Background(), entry.runtime, retainChildScratch, releaseRetirement)
	}
	return true
}

// idleReleasePregatesClear reports whether every precondition that must hold
// BEFORE a non-terminal teardown holds for this session: no queued
// notifications, no pending watch sends, and no job-manager runtime
// obligations (running jobs or a pending terminal flush) that
// releaseQuiescentRuntime would refuse on mid-release, after the single
// teardown pass was already spent.
func (s *Session) idleReleasePregatesClear() bool {
	if s.peekNotifications() != 0 {
		return false
	}
	if s.jobManager != nil && (s.jobManager.hasPendingWatchSends() || s.jobManager.hasRuntimeObligations()) {
		return false
	}
	return true
}
