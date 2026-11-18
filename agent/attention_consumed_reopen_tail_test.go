package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestARecoveredOwedTriggerEndsTheRequestThatCarriesIt pins the order a restart
// owes after a crash between the consumed marker and RunStarted.
//
// A fresh wake releases an older reopened report, the drive selects that report
// and its consumed marker lands, and the process dies before the owed
// generation's RunStarted commits. Bootstrap re-arms that generation and
// restores its trigger from the transcript, then makes the first request.
//
// Two things must be true of that request, and both are the same fact — the
// trigger is the row the recovered generation exists to present:
//
//   - No automatic-drive hold survives on it. A hold is a stop's quiet, and a
//     stop is older evidence than the wake this generation is running for. A
//     request that filters its own trigger out settles successfully with the
//     report durably consumed and never read.
//   - Its turn ends the history. The reopen appended its turn where the settle
//     left it, and any exchanges from generations that ran while the quiet
//     stood sit after it. Restoring in transcript order would leave the request
//     ending on those exchanges instead of on the report it owes.
//
// The topic rows do not move: a resolved row a completed generation already
// presented keeps the position that request answered for.
func TestARecoveredOwedTriggerEndsTheRequestThatCarriesIt(t *testing.T) {
	sess, reopenedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	stoppedGeneration := settledGeneration(t, sess)
	// A newer wake releases the stop's quiet for this row.
	journalDeriveHeldRelease(t, sess, stoppedGeneration)
	// The crash window: the drive selects the released report and its consumed
	// marker lands, then the process dies before the owed generation's
	// RunStarted commits.
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
	if got := fold.resolutions[reopenedID]; got != delegateAttentionConsumed {
		t.Fatalf("premise broken: %q resolved %q, want consumed", reopenedID, got)
	}
	reportTurn, ok := fold.turns[reopenedID]
	if !ok {
		t.Fatalf("premise broken: the restored fold holds no turn for %q", reopenedID)
	}

	// Resume in transcript order and hold a report the stop quieted, then put a
	// later exchange after it: exactly what a generation that ran while the
	// quiet stood appends.
	restored.mu.Lock()
	restored.history = append(restored.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("history before the report")))
	restored.mu.Unlock()

	// The derivation is the pass that would rebuild the quiet, and the trigger
	// is where bootstrap admits it. A hold on the trigger here is the defect.
	restored.deriveHeldDelegateAttentionFromOutcome()
	restored.holdDelegateAttentionFromAutomaticDriveAt(reopenedID, stoppedGeneration)

	restored.mu.Lock()
	restored.history = append(restored.history, reportTurn)
	restored.history = append(restored.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("exchange after the released report")))
	restored.mu.Unlock()

	turns := []schema.Turn{reportTurn}
	if err := restored.restoreAcceptedTrigger(reopenedID, turns); err != nil {
		t.Fatalf("restore the recovered trigger: %v", err)
	}

	restored.attentionMu.Lock()
	_, held := restored.attentionAutomaticDriveHolds[reopenedID]
	restored.attentionMu.Unlock()
	if held {
		t.Fatalf("the recovered trigger %q is still held: the request that settles this generation filters out the report it exists to present, so the run completes with the report durably consumed and never read", reopenedID)
	}

	restored.mu.Lock()
	last := ""
	if len(restored.history) != 0 {
		last = restored.history[len(restored.history)-1].AttentionID
	}
	restored.mu.Unlock()
	if last != reopenedID {
		t.Fatalf("the restored history ends on %q, want the recovered trigger %q at the tail: the request that settles this generation ends ahead of the report it owes", last, reopenedID)
	}
}

// TestARestoreKeepsACompletedGenerationsReportWhereItWasPresented pins the other
// direction of the tail move, which is the harm the move's restriction prevents.
//
// A release is owed a tail position only while the row it freed has not been
// presented. Once a generation's request carried the report, that position is
// answered for: the reply that addressed it sits after it, and moving the report
// behind that reply reorders history an ordinary reclaim must leave alone. The
// crash recovery's own row is the exception because the generation it recovers
// has no RunStarted, so nothing was answered for at any position.
func TestARestoreKeepsACompletedGenerationsReportWhereItWasPresented(t *testing.T) {
	sess, reopenedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	stoppedGeneration := settledGeneration(t, sess)
	journalDeriveHeldRelease(t, sess, stoppedGeneration)
	// The row was presented and its generation completed: the marker is written
	// and the run that carried it settled.
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
	reportTurn, ok := fold.turns[reopenedID]
	if !ok {
		t.Fatalf("premise broken: the restored fold holds no turn for %q", reopenedID)
	}

	const replyText = "the reply that addressed the report"
	restored.mu.Lock()
	restored.history = append(restored.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("history before the report")))
	restored.history = append(restored.history, reportTurn)
	restored.history = append(restored.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(replyText)))
	restored.mu.Unlock()

	restored.deriveHeldDelegateAttentionFromOutcome()

	restored.mu.Lock()
	defer restored.mu.Unlock()
	for index, turn := range restored.history {
		if turn.AttentionID != reopenedID {
			continue
		}
		if index == len(restored.history)-1 {
			t.Fatalf("an ordinary reclaim moved the already-presented report %q to the tail, behind the reply that addressed it: a release is owed a position only while the row it freed has not been presented", reopenedID)
		}
		if restored.history[index+1].Message.Content[0].Text != replyText {
			t.Fatalf("the presented report %q no longer sits directly before the reply that addressed it", reopenedID)
		}
		return
	}
	t.Fatalf("premise broken: the resolved report %q is not in the restored history", reopenedID)
}
