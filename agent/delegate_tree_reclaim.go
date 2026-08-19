package agent

import (
	"errors"
	"fmt"
	"sort"
	"time"

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
	for id, aggregate := range c.durable {
		if c.isResidentTerminalRuntimeLocked(id, aggregate) {
			resident++
		}
	}
	needed := resident + required - c.maxRetainedTerminal
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
		parentID := aggregate.Descriptor.ParentDelegateID
		if parentID == "" {
			entry.ownerRuntime = c.rootRuntime
		} else if parent := c.live[parentID]; parent != nil {
			entry.ownerRuntime = parent.runtime
		}
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
			entry.runtime.Close()
		}
		closed[entry.delegateID] = entry.runtime
	}
	if err := s.delegateController.CompleteRuntimeReclamation(claim, closed); err != nil {
		return err
	}
	completed = true
	return nil
}
