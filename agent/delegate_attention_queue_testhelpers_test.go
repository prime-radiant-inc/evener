package agent

// Test-only adapters for the durable attention queue. Production code drives the
// queue through the controller methods; these helpers keep unit tests that used
// to poke the deleted wake maps reading and writing the same durable rows.

func (c *delegateTreeController) noteDelegateAttentionForTest(delegateID, attentionID string) bool {
	if c == nil || delegateID == "" || attentionID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.noteDelegateAttentionLockedForTest(delegateID, attentionID)
}

// noteDelegateAttentionLockedForTest is noteDelegateAttentionForTest for a
// caller that already holds c.mu.
func (c *delegateTreeController) noteDelegateAttentionLockedForTest(delegateID, attentionID string) bool {
	if c == nil || delegateID == "" || attentionID == "" {
		return false
	}
	if c.durable[delegateID] == nil {
		return false
	}
	if c.delegateAttentionCommittedLocked(delegateID, attentionID) {
		return false
	}
	if _, err := c.appendLocked(attentionQueuedEvent(delegateID, attentionID, true)); err != nil {
		return false
	}
	c.resetDelegateAttentionAttemptsLocked(delegateID)
	return true
}

// attentionWakeIDsForTest reads delegateID's durable queue IDs with the
// controller lock held.
func (c *delegateTreeController) attentionWakeIDsForTest(delegateID string) map[string]struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.delegateAttentionWakeIDsLocked(delegateID)
}

// attentionCommittedWakeIDsForTest reads the IDs whose durable row has already
// published a wake.
func (c *delegateTreeController) attentionCommittedWakeIDsForTest(delegateID string) map[string]struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		return nil
	}
	ids := make(map[string]struct{}, len(aggregate.PendingAttention))
	for _, row := range aggregate.PendingAttention {
		if row.Committed {
			ids[row.AttentionID] = struct{}{}
		}
	}
	return ids
}

// seedAttentionRowForTest appends one durable queue row through the journal, so
// tests that used to seed delegateAttentionState.wakeIDs directly exercise the
// same apply path production does.
func (c *delegateTreeController) seedAttentionRowForTest(delegateID, attentionID string, committed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.appendLocked(attentionQueuedEvent(delegateID, attentionID, committed))
}
