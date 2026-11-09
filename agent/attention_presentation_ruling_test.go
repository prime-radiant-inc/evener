package agent

import (
	"context"
	"errors"
	"testing"
)

// The 2026-11-09 lifecycle ruling prefers a possible duplicate over a lost
// delegate report (issue #5674). attentionRunPresentedItsReport therefore
// treats every unlisted failure as "presentation unproven" and reopens the
// report, even though a tool round that did carry it can then present the same
// content twice. Only an ordinary settled run and budget exhaustion prove the
// request ran.
func TestAttentionPresentationStaysLossProof(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		errors.New("a tool round failed after the report may have been carried"),
		context.Canceled,
		errors.New("transcript append failed"),
	} {
		if attentionRunPresentedItsReport(err) {
			t.Fatalf("%v was classified as presented; an unproven presentation must reopen rather than risk silent loss", err)
		}
	}
	if !attentionRunPresentedItsReport(nil) {
		t.Fatal("an ordinary settled run must count as presented")
	}
	if !attentionRunPresentedItsReport(&budgetExhaustionError{
		Budget:    exhaustedBudgetToolRounds,
		Limit:     1,
		Resumable: true,
	}) {
		t.Fatal("budget exhaustion happens after the call ran and must count as presented")
	}
}
