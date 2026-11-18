package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestAConsumedReopenTheReleaseFreedStillMovesToTheTail pins the order the
// release owes after a crash between the consumed marker and RunStarted.
//
// A fresh wake releases an older reopened report, the drive selects that report
// and its consumed marker lands, and the process dies before the owed
// generation's RunStarted commits. On restart the row is resolved, so the
// derivation's quiet pass — which walks pending rows — never looks at it. The
// owed generation's trigger is restored in transcript order, which puts the
// first request ahead of the frame that released the report instead of ending
// on it. The report is delivered but out of order, and the request that
// presents the frame omits the report the frame freed.
//
// The row is resolved consumed but its release is still owed its tail position,
// so the derivation must include it. Other resolved rows keep their positions.
func TestAConsumedReopenTheReleaseFreedStillMovesToTheTail(t *testing.T) {
	sess, reopenedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	stoppedGeneration := settledGeneration(t, sess)
	// A newer wake releases the stop's quiet for this row.
	journalDeriveHeldRelease(t, sess, stoppedGeneration)
	// The crash window: the drive selects the released report and its consumed
	// marker lands, then the process dies before the owed generation's
	// RunStarted commits. The marker is written on the pre-crash session, which
	// is the transcript the restore folds.
	if err := sess.resolveAttentionDurablyForGeneration([]string{reopenedID}, delegateAttentionConsumed, stoppedGeneration); err != nil {
		t.Fatalf("mark the reopen consumed: %v", err)
	}

	restored := coldRestoredDeriveSession(t, sess, journalPath)
	restored.attentionMu.Lock()
	fold, err := restored.delegateAttentionFoldLocked()
	restored.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("fold after the marker: %v", err)
	}
	if _, resolved := fold.resolutions[reopenedID]; !resolved {
		t.Fatalf("premise broken: %q is not resolved, so this does not exercise a consumed row", reopenedID)
	}
	if got := fold.resolutions[reopenedID]; got != delegateAttentionConsumed {
		t.Fatalf("premise broken: %q resolved %q, want consumed", reopenedID, got)
	}

	// The resumed history carries the report's own turn at its original
	// position, then a later exchange — exactly what a generation that ran while
	// the quiet stood would append after it. The turn is read from the fold so
	// the fixture holds the same row the transcript does.
	restored.attentionMu.Lock()
	fold, err = restored.delegateAttentionFoldLocked()
	restored.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("fold the restored transcript: %v", err)
	}
	reportTurn, ok := fold.turns[reopenedID]
	if !ok {
		t.Fatalf("premise broken: the restored fold holds no turn for %q, so there is nothing to move", reopenedID)
	}
	restored.mu.Lock()
	restored.history = append(restored.history, reportTurn)
	restored.history = append(restored.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("exchange after the released report")))
	restored.mu.Unlock()

	restored.deriveHeldDelegateAttentionFromOutcome()

	restored.mu.Lock()
	last := ""
	if len(restored.history) != 0 {
		last = restored.history[len(restored.history)-1].AttentionID
	}
	restored.mu.Unlock()
	if last != reopenedID {
		t.Fatalf("after the reclaim the history ends on %q, want the released report %q at the tail: a consumed reopen whose release is still owed must move, or the request that presents the frame that freed it ends ahead of the report", last, reopenedID)
	}
}
