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
		t.Fatalf("a delegate that owes no attention kept attention state: rows=%v failures=%d parked=%v turn=%d",
			c.durable["dlg_stale"].PendingAttention, state.restoreFailures, state.parked, state.driveTurn)
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
	if got := c.delegateAttentionWakeIDsLocked("dlg_owed"); len(got) == 0 {
		t.Fatalf("reconcile did not rebuild the owed attention id: %v", got)
	} else if _, owed := got[attentionID]; !owed {
		t.Fatalf("reconcile did not rebuild the owed attention id: %v", got)
	}
}

// The bootstrap rebuilds a pre-queue journal from the transcript, and a child
// delegate's transcript keeps a presented report's message after the journal
// resolved it. Backfilling that id would present a settled report a second time
// on every restart, so only an id the journal never resolved is a pre-upgrade
// row.
func TestReconcileDelegateAttentionBackfillsOnlyUnjournaledRows(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_owed", "")
	const (
		presentedID = "delegate:presented"
		legacyID    = "delegate:legacy-pending"
	)
	writer, err := transcript.NewWriter(transcriptPath(c.stateDir, "child-dlg_owed"), transcript.Header{SessionID: "child-dlg_owed"})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	for _, attentionID := range []string{presentedID, legacyID} {
		turn := schema.NewTurn(schema.TurnSteering, llm.User(attentionID))
		turn.AttentionID = attentionID
		if err := writer.AppendDurable(turn); err != nil {
			t.Fatalf("append attention %s: %v", attentionID, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	// The journal knows the presented row and that it settled; the legacy row was
	// never journaled at all (a pre-queue journal).
	c.mu.Lock()
	if _, err := c.appendLocked(
		attentionQueuedEvent("dlg_owed", presentedID, true),
		attentionResolvedEvent("dlg_owed", presentedID),
	); err != nil {
		c.mu.Unlock()
		t.Fatalf("seed journal: %v", err)
	}
	c.mu.Unlock()

	if err := c.reconcileDelegateAttentionFromTranscripts(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable["dlg_owed"]
	if _, ok := aggregate.ResolvedAttention[presentedID]; !ok {
		t.Fatalf("resolved set = %v, want the presented id to stay resolved", aggregate.ResolvedAttention)
	}
	got := make(map[string]struct{}, len(aggregate.PendingAttention))
	for _, row := range aggregate.PendingAttention {
		got[row.AttentionID] = struct{}{}
	}
	if _, requeued := got[presentedID]; requeued {
		t.Fatalf("bootstrap requeued a journal-resolved row: %v", aggregate.PendingAttention)
	}
	if _, ok := got[legacyID]; !ok {
		t.Fatalf("bootstrap dropped a genuinely unjournaled legacy row: %v", aggregate.PendingAttention)
	}
	if !aggregate.NeedsAttention {
		t.Fatal("the backfilled legacy row raised no NeedsAttention")
	}
}
