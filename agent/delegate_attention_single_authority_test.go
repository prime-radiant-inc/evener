package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestResolvingAttentionUpdatesQueueAndProjectionAtomically pins the Slice 1
// invariant that removes the Round-5/Round-6 split-state Catch-22: the durable
// queue row and the NeedsAttention projection move in the same Apply, so no
// later clear pass can observe a resolved row whose wake eligibility still
// stands, or a cleared flag whose row still owes a wake.
func TestResolvingAttentionUpdatesQueueAndProjectionAtomically(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_atomic", "")
	c.seedAttentionRowForTest("dlg_atomic", "att_atomic", true)

	c.mu.Lock()
	before := c.durable["dlg_atomic"]
	if len(before.PendingAttention) != 1 || !before.NeedsAttention {
		c.mu.Unlock()
		t.Fatalf("seeded state = rows %v needs %v, want one owed row raising attention", before.PendingAttention, before.NeedsAttention)
	}
	c.mu.Unlock()

	// The resolution is one journaled event, applied by the store's single
	// Apply boundary. Nothing between the two reads can run a separate clear.
	if err := c.appendAttentionResolvedLocked("dlg_atomic", map[string]struct{}{"att_atomic": {}}); err != nil {
		t.Fatalf("resolve attention: %v", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	after := c.durable["dlg_atomic"]
	if len(after.PendingAttention) != 0 {
		t.Fatalf("resolved row still queued: %v", after.PendingAttention)
	}
	if after.NeedsAttention {
		t.Fatal("resolved row still raises NeedsAttention: a separate clear pass would be needed")
	}
	if len(c.delegateAttentionWakeIDsLocked("dlg_atomic")) != 0 {
		t.Fatal("resolved row still reports a wake: the Catch-22 window is reachable")
	}
	// Replaying the same journal must reach the same single-authority state.
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("load journal: %v", err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatalf("fold journal: %v", err)
	}
	replayed := state["dlg_atomic"]
	if replayed == nil {
		t.Fatal("replayed state lost the delegate")
	}
	if len(replayed.PendingAttention) != 0 || replayed.NeedsAttention {
		t.Fatalf("replayed state = rows %v needs %v, want resolved in one fold", replayed.PendingAttention, replayed.NeedsAttention)
	}
}
