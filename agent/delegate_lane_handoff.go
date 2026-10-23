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

// beginSubtreeLaneHandoff fences delegateID and every delegate beneath it for
// the duration of a lane handoff. It refuses when the subtree already has a
// start reservation, an active durable generation, a cold-restore window or
// attention-restore hold, or a handoff in flight anywhere inside or above it.
// A true return MUST be paired with endSubtreeLaneHandoff.
func (c *delegateTreeController) beginSubtreeLaneHandoff(delegateID string) bool {
	if c == nil || delegateID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return false
	}
	members := c.subtreeMembersLocked(delegateID)
	// An ancestor's handoff already covers this subtree, and a handoff rooted
	// inside it would be torn down by this one.
	if c.laneHandoffCoversLocked(delegateID) {
		return false
	}
	for root := range c.laneHandoffs {
		if _, covered := members[root]; covered {
			return false
		}
	}
	// A committed start is durable before its runtime attaches, so an active
	// generation closes the commit-to-attach half of the send path. The
	// reservation scan below covers the pre-commit half.
	if c.stableWorktreeSubtreeActiveLocked(members) {
		return false
	}
	// A reservation for an existing member can install after its commit. A
	// create reservation's new delegate is not durable yet, so it is not a
	// subtree member; the descriptor names the parent that makes it one.
	for _, record := range c.reservations {
		if record == nil {
			continue
		}
		if _, covered := members[record.delegateID]; covered {
			return false
		}
		if !record.create || record.descriptor.ParentDelegateID == "" {
			continue
		}
		if _, covered := members[record.descriptor.ParentDelegateID]; covered {
			return false
		}
	}
	// A cold restore window covers the commit-to-install gap, which leaves no
	// durable trace the checks above could see. A wake pass that restored a
	// runtime and has not yet decided its reservation owns it until it does;
	// reclamation already honors the same hold for the same reason.
	for id := range members {
		if c.laneRestores[id] > 0 || c.attentionRestoreHeldLocked(id) {
			return false
		}
	}
	if c.laneHandoffs == nil {
		c.laneHandoffs = make(map[string]struct{})
	}
	c.laneHandoffs[delegateID] = struct{}{}
	return true
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

// controllerClosing reports whether the controller has fenced admission for
// its own close. Callers use it to distinguish that case from a mid-tree
// session close, which still needs the lane-handoff fence.
func (c *delegateTreeController) controllerClosing() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closing
}

// laneHandoffCoveredByAncestor reports whether an in-flight handoff rooted at a
// strict ancestor already fences delegateID. A close-time disposal running
// inside that handoff can proceed under the ancestor's fence; refusing it would
// retain (and lock) a nested lane on every explicit dispose of a delegate whose
// child session owns its own lanes.
func (c *delegateTreeController) laneHandoffCoveredByAncestor(delegateID string) bool {
	if c == nil || delegateID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.laneHandoffCoveredByAncestorLocked(delegateID)
}

// laneHandoffCoveredByAncestorLocked is the locked form. Callers must hold c.mu.
func (c *delegateTreeController) laneHandoffCoveredByAncestorLocked(delegateID string) bool {
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return false
	}
	for id := aggregate.Descriptor.ParentDelegateID; id != ""; {
		if _, covered := c.laneHandoffs[id]; covered {
			return true
		}
		parent := c.durable[id]
		if parent == nil {
			return false
		}
		id = parent.Descriptor.ParentDelegateID
	}
	return false
}

// laneHandoffCoversLocked reports whether delegateID or any of its ancestors
// is the root of an in-flight lane handoff. Callers must hold c.mu.
func (c *delegateTreeController) laneHandoffCoversLocked(delegateID string) bool {
	if c == nil || delegateID == "" {
		return false
	}
	for id := delegateID; id != ""; {
		if _, covered := c.laneHandoffs[id]; covered {
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
	if c.laneRestores[delegateID] <= 1 {
		delete(c.laneRestores, delegateID)
		return
	}
	c.laneRestores[delegateID]--
}
