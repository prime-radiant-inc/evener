package agent

import "testing"

// reconcileDelegateAttentionFromTranscripts is the bootstrap boundary that
// rebuilds owed attention from transcripts. A delegate that no longer owes
// attention must leave the drive's line entirely: its per-delegate attention
// state — wake IDs, restore failures, park and drive turn — goes with it, so a
// reconcile cannot strand a turn, a park or a failure count for a delegate that
// owes nothing.
func TestReconcileDelegateAttentionDropsStateForDelegatesThatOweNothing(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.mu.Lock()
	c.attention = map[string]*delegateAttentionState{
		"dlg_stale": {driveTurn: 7, parked: true, restoreFailures: 3},
	}
	c.mu.Unlock()
	if err := c.reconcileDelegateAttentionFromTranscripts(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.attention["dlg_stale"]; state != nil {
		t.Fatalf("a delegate that owes no attention kept attention state: wakeIDs=%v failures=%d parked=%v turn=%d",
			state.wakeIDs, state.restoreFailures, state.parked, state.driveTurn)
	}
}
