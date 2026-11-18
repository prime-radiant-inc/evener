package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A request built between a fresh arm's wake and the arm's release callback
// must not present the fresh row while filtering out the report that same wake
// freed. Only the drive-side ID list reconciled the journal until now, so the
// model projection filtered the raw hold map: the released report stayed out of
// the request and the fresh row presented alone, one generation late for the
// reader. The projection must ask the same released-floor evidence the drive
// asks, at the point it reads the holds.
func TestHeldTurnProjectionReleasesOnTheJournaledFloor(t *testing.T) {
	sess, reopenedID, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	sess.deriveHeldDelegateAttentionFromOutcome()

	heldTurn := schema.NewTurn(schema.TurnSteering, llm.User(acceptedTriggerText))
	heldTurn.AttentionID = reopenedID
	turns := []schema.Turn{heldTurn}

	// The stop's quiet stands and nothing has released it: the projection keeps
	// the row's turn out of the automatic drive's history.
	kept, dropped := sess.dropHeldDelegateAttentionTurns(turns)
	if len(dropped) != 1 || len(kept) != 0 {
		t.Fatalf("an unreleased hold was kept in the projection (kept %d, dropped %d): the user's stop no longer holds its row out of the automatic drive", len(kept), len(dropped))
	}

	// A fresh arm admits its wake at the settled generation and journals the
	// release; its release callback has not run, so the hold map still names
	// the row.
	journalDeriveHeldRelease(t, sess, 1)
	kept, dropped = sess.dropHeldDelegateAttentionTurns(turns)
	if len(dropped) != 0 || len(kept) != 1 {
		t.Fatalf("the projection dropped a released row's turn (kept %d, dropped %d): the request presents the fresh row alone and leaves the report the same wake freed for a later generation", len(kept), len(dropped))
	}
}
