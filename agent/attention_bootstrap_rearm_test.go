package agent

import (
	"maps"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestBootstrapRearmsAncestorQuietAfterItNotesTheWake pins the rule that the
// pass rebuilding the wake set is the pass ending the holds it supersedes.
//
// A daemon restart that must recover a descendant's owed start rebuilds the idle
// ancestor first, and rebuilding it derives the quiet its stop left: the stop's
// own row is held, so the ancestor does not present it. Bootstrap then reconciles
// the transcripts, which rebuilds the controller's wake set from what they still
// owe. That note is the restart's re-arm — the report is meant to present now.
//
// Nothing journals a release for the note, because the run it supersedes never
// settled, so presentation's reconcile against release generations can never end
// the derived hold. The ancestor keeps suppressing the very report the restart
// re-armed.
//
// The earlier version of this test called the derivation a second time to make
// its assertion pass, and the version after that called the re-arm helper
// directly. Both supplied the condition they were meant to observe: they passed
// with production's path unchanged. This version drives the one production
// function that owns the notes — reconcileDelegateAttentionFromTranscripts — and
// nothing else, so it fails when that function stops re-arming.
//
// The invariant: the pass that rebuilds the wake set is also the pass that ends a
// derived hold those wakes supersede, and it runs before any deferred work.
func TestBootstrapRearmsAncestorQuietAfterItNotesTheWake(t *testing.T) {
	t.Parallel()
	sess, reopenedID, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The ancestor's runtime is rebuilt before reconciliation: this derivation is
	// the one production runs at that point, and the controller has no wake noted
	// for the report yet, so the quiet is installed.
	sess.deriveHeldDelegateAttentionFromOutcome()
	sess.attentionMu.Lock()
	heldGeneration, installed := sess.attentionAutomaticDriveHolds[reopenedID]
	sess.attentionMu.Unlock()
	if !installed {
		t.Fatalf("premise broken: the rebuilt ancestor installed no hold for %q, so there is no stale quiet to end", reopenedID)
	}
	// Reconciliation folds each eligible delegate's own transcript, so the row
	// must be pending there — which is what the restart's transcript holds. The
	// path comes from the delegate's own ref, the same way reconcile resolves it.
	controller.mu.Lock()
	transcriptRef := ""
	if aggregate := controller.durable["dlg_hold"]; aggregate != nil {
		transcriptRef = aggregate.Descriptor.TranscriptRef
	}
	stateDir := controller.stateDir
	controller.mu.Unlock()
	delegatePath, delegateSessionID, err := delegateTranscriptPathFromRef(stateDir, transcriptRef)
	if err != nil {
		t.Fatalf("resolve the delegate's transcript ref %q: %v", transcriptRef, err)
	}
	appendColdAttentionRow(t, delegatePath, delegateSessionID, reopenedID, "the report the stop reopened")
	// The row is pending in the transcript, so reconciliation rebuilds the wake
	// set with it. That note is the restart's re-arm, and this is the production
	// pass that owns it.
	if err := controller.reconcileDelegateAttentionFromTranscripts(); err != nil {
		t.Fatalf("reconcile delegate attention from transcripts: %v", err)
	}
	noted := func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		state := controller.attention["dlg_hold"]
		if state == nil {
			return false
		}
		_, wake := state.wakeIDs[reopenedID]
		return wake
	}()
	if !noted {
		t.Fatalf("premise broken: reconciliation did not note %q as an owed wake, so this pass has no re-arm to read", reopenedID)
	}

	automatic, err := sess.automaticDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("automatic attention: %v", err)
	}
	if !slices.Contains(automatic, reopenedID) {
		sess.attentionMu.Lock()
		holds := maps.Clone(sess.attentionAutomaticDriveHolds)
		sess.attentionMu.Unlock()
		t.Fatalf("the re-armed report %q is still withheld after bootstrap rearmed it: the ancestor keeps suppressing the report the restart re-armed (hold=%d installed, holds now %v, automatic=%v)", reopenedID, heldGeneration, holds, automatic)
	}
}
