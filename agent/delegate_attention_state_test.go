package agent

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

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

// A delegate that still owes attention keeps its other per-delegate state
// across the reconcile rebuild: the drive's turn survives, so a repeated
// reconcile does not reset every delegate to equal priority.
func TestReconcileDelegateAttentionPreservesStateForDelegatesThatStillOwe(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_owed", "")
	const attentionID = "delegate:still-owed"
	writer, err := transcript.NewWriter(transcriptPath(c.stateDir, "child-dlg_owed"), transcript.Header{SessionID: "child-dlg_owed"})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	turn := schema.NewTurn(schema.TurnSteering, llm.User("still owed"))
	turn.AttentionID = attentionID
	if err := writer.AppendDurable(turn); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	c.mu.Lock()
	c.attentionStateLocked("dlg_owed").driveTurn = 7
	c.mu.Unlock()
	if err := c.reconcileDelegateAttentionFromTranscripts(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.attention["dlg_owed"]
	if state == nil {
		t.Fatalf("reconcile dropped a delegate that still owes attention")
	}
	if state.driveTurn != 7 {
		t.Fatalf("reconcile reset the drive turn of a still-owed delegate: got %d, want 7", state.driveTurn)
	}
	if _, owed := state.wakeIDs[attentionID]; !owed {
		t.Fatalf("reconcile did not rebuild the owed attention id: %v", state.wakeIDs)
	}
}
