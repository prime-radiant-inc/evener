package agent

// Controller-state helpers for the parent-only non-destructive lane unlock
// (issue #481): deciding whether a descendant runtime still occupies the lane,
// and detaching the exact runtime after its non-terminal release.

// delegateHasResidentDescendants reports whether any strict descendant of
// delegateID still has a resident runtime in that delegate's lane. Unlock
// evicts only the delegate's own runtime; a descendant sharing the lane would
// keep running in it after the lock is released, so the operation refuses until
// the subtree is quiet. A nested isolated delegate has its own lane and does
// not block this one.
func (c *delegateTreeController) delegateHasResidentDescendants(delegateID string) bool {
	if c == nil || delegateID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.subtreeMembersLocked(delegateID) {
		if id == delegateID || c.residentDelegateRuntimeLocked(id) == nil {
			continue
		}
		if c.descendantSharesLaneLocked(id, delegateID) {
			return true
		}
	}
	return false
}

// descendantSharesLaneLocked reports whether descendantID runs in laneOwner's
// isolation lane: walking up to its nearest worktree-isolated ancestor reaches
// laneOwner. A nested isolated delegate returns false (it has its own lane).
// Callers hold c.mu.
func (c *delegateTreeController) descendantSharesLaneLocked(descendantID, laneOwner string) bool {
	for id := descendantID; id != ""; {
		aggregate := c.durable[id]
		if aggregate == nil {
			return false
		}
		if aggregate.Descriptor.Isolation == "worktree" && aggregate.Descriptor.WorkingDir != "" {
			return id == laneOwner
		}
		id = aggregate.Descriptor.ParentDelegateID
	}
	return false
}

// detachReleasedLaneRuntime clears the controller's pointers to runtime after
// its non-terminal release, but only where they still name that exact runtime.
// A replacement installed after the release is left alone. The delegate stays
// durable and resumable; a later send cold-restores a fresh runtime, so a
// stale pointer here would collide with that restore in AttachRuntime.
func (c *delegateTreeController) detachReleasedLaneRuntime(delegateID string, runtime *Session) {
	if c == nil || delegateID == "" || runtime == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live[delegateID]
	if live == nil {
		return
	}
	// The same invariant reclamation uses: a still-open binding owns its
	// runtime, and a released binding's exact resident pointer is ours to clear.
	if live.binding == nil && live.runtime == runtime {
		c.setResidentRuntimeLocked(live, nil)
		c.evidenceVersion++
	}
}
