package agent

// A worktree-lane handoff unlocks and removes a delegate's isolation lane.
// Everything that can put a runtime into that lane must be excluded for the
// whole critical section, not only at the point-in-time quiescence read:
//
//   - the target's descendants share the lane when they are non-isolated, and
//   - a cold attention restore commits without writing a durable event, so a
//     durable subtree check cannot see its commit-to-install window.
//
// The fence below is process-local controller state under mu. A handoff is a
// map entry held logically across the teardown; no mutex is held across git,
// filesystem, session or job-manager work.

// laneHandoffAdmission is the outcome of beginSubtreeLaneHandoff.
type laneHandoffAdmission uint8

const (
	// laneHandoffAcquired: the caller owns the fence and must release it.
	laneHandoffAcquired laneHandoffAdmission = iota
	// laneHandoffAlreadyCovered: the controller is closing or a strict ancestor's
	// handoff already excludes admissions into this subtree. Nothing was recorded;
	// a close-time disposal may proceed under that existing coverage.
	laneHandoffAlreadyCovered
	// laneHandoffBusy: work is in flight in this lane's own subtree. The caller
	// must not proceed.
	laneHandoffBusy
)

// beginSubtreeLaneHandoff fences delegateID and every delegate beneath it for
// the duration of a lane handoff. It returns laneHandoffBusy when the subtree
// already has a start reservation, an active durable generation, a cold-restore
// window or attention-restore hold, or a handoff rooted inside it.
// laneHandoffAlreadyCovered means closing or an ancestor handoff already fences
// the subtree, so the caller needs no fence of its own. A laneHandoffAcquired
// return MUST be paired with endSubtreeLaneHandoff.
func (c *delegateTreeController) beginSubtreeLaneHandoff(delegateID string) laneHandoffAdmission {
	if c == nil || delegateID == "" {
		return laneHandoffBusy
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return laneHandoffAlreadyCovered
	}
	members := c.subtreeMembersLocked(delegateID)
	if _, self := c.laneHandoffs[delegateID]; self {
		return laneHandoffBusy
	}
	if c.laneHandoffCoveredByAncestorLocked(delegateID) {
		return laneHandoffAlreadyCovered
	}
	// A handoff rooted inside this subtree would be torn down by this one.
	for root := range c.laneHandoffs {
		if _, covered := members[root]; covered {
			return laneHandoffBusy
		}
	}
	// A committed start is durable before its runtime attaches, so an active
	// generation closes the commit-to-attach half of the send path. The
	// reservation scan below covers the pre-commit half.
	if c.stableWorktreeSubtreeActiveLocked(members) {
		return laneHandoffBusy
	}
	// A reservation for an existing member can install after its commit. A
	// create reservation's new delegate is not durable yet, so it is not a
	// subtree member; the descriptor names the parent that makes it one.
	for _, record := range c.reservations {
		if record == nil {
			continue
		}
		if delegateIDInSet(record.delegateID, members) {
			return laneHandoffBusy
		}
		if !record.create || record.descriptor.ParentDelegateID == "" {
			continue
		}
		if delegateIDInSet(record.descriptor.ParentDelegateID, members) {
			return laneHandoffBusy
		}
	}
	// A cold restore window covers the commit-to-install gap, which leaves no
	// durable trace the checks above could see. A wake pass that restored a
	// runtime and has not yet decided its reservation owns it until it does;
	// reclamation already honors the same hold for the same reason.
	for id := range members {
		if c.laneRestores[id] > 0 || c.attentionRestoreHeldLocked(id) {
			return laneHandoffBusy
		}
	}
	if c.laneHandoffs == nil {
		c.laneHandoffs = make(map[string]struct{})
	}
	c.laneHandoffs[delegateID] = struct{}{}
	return laneHandoffAcquired
}

// endSubtreeLaneHandoff reverses beginSubtreeLaneHandoff. Releasing an
// unheld id is a no-op, so a refusal path cannot clear another handoff.
func (c *delegateTreeController) endSubtreeLaneHandoff(delegateID string) {
	if c == nil || delegateID == "" {
		return
	}
	c.mu.Lock()
	delete(c.laneHandoffs, delegateID)
	c.mu.Unlock()
}

// laneHandoffCoveredByAncestorLocked reports whether an in-flight handoff
// rooted at a strict ancestor already fences delegateID. Callers must hold c.mu.
func (c *delegateTreeController) laneHandoffCoveredByAncestorLocked(delegateID string) bool {
	if c == nil || delegateID == "" || len(c.laneHandoffs) == 0 {
		return false
	}
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return false
	}
	return c.laneHandoffCoversLocked(aggregate.Descriptor.ParentDelegateID)
}

// laneHandoffCoversLocked reports whether delegateID or any of its ancestors
// is the root of an in-flight lane handoff. Callers must hold c.mu.
func (c *delegateTreeController) laneHandoffCoversLocked(delegateID string) bool {
	if c == nil || delegateID == "" {
		return false
	}
	if len(c.laneHandoffs) == 0 {
		return false
	}
	if _, covered := c.laneHandoffs[delegateID]; covered {
		return true
	}
	return c.laneHandoffCoveredByAncestorLocked(delegateID)
}

// beginLaneRestore opens a counted cold-restore window for delegateID before
// its commit. The window spans runtime installation so a lane handoff either
// sees it and refuses, or cannot start until it closes. It MUST be paired with
// endLaneRestore.
func (c *delegateTreeController) beginLaneRestore(delegateID string) error {
	if c == nil || delegateID == "" {
		return errDelegateTargetBusy
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.laneHandoffCoversLocked(delegateID) {
		return errDelegateTargetBusy
	}
	if c.laneRestores == nil {
		c.laneRestores = make(map[string]int)
	}
	c.laneRestores[delegateID]++
	return nil
}

// endLaneRestore closes one cold-restore window opened by beginLaneRestore.
func (c *delegateTreeController) endLaneRestore(delegateID string) {
	if c == nil || delegateID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	releaseCountedLocked(c.laneRestores, delegateID)
}

// releaseCountedLocked drops one reference from a counted map and deletes the
// entry when the last reference leaves. Callers must hold c.mu.
func releaseCountedLocked(counts map[string]int, delegateID string) {
	if counts[delegateID] <= 1 {
		delete(counts, delegateID)
		return
	}
	counts[delegateID]--
}
