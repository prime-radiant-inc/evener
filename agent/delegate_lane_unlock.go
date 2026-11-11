package agent

// Controller-state helpers for the parent-only non-destructive lane unlock
// (issue #481): deciding whether a descendant runtime still occupies the lane,
// and detaching the exact runtime after its non-terminal release.

// delegateHasResidentDescendants reports whether any strict descendant of
// delegateID still has a resident runtime. Unlock evicts only the delegate's
// own runtime: a descendant would stay in the released runtime's registry and
// an AttachRuntime for it would later be rejected, so the operation refuses
// until the whole subtree is quiet.
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
		return true
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
	if c.clearResidentRuntimeIfExactLocked(delegateID, runtime) {
		c.evidenceVersion++
	}
}
