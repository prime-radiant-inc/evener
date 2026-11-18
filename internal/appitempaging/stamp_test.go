package appitempaging

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestOmitResolvedAskCallsSkipsMeasurementAtZeroLimit pins the recovery paths'
// contract: a zero limit omits the list as a unit without measuring it first
// (#6323). The recovery paths run only on a page that is already over budget,
// so serializing the list there would pay the cost of the thing being thrown
// away.
func TestOmitResolvedAskCallsSkipsMeasurementAtZeroLimit(t *testing.T) {
	t.Parallel()
	// A list whose encoding is larger than any limit a caller would pass, so a
	// measurement-first implementation would keep it at a zero limit only if it
	// compared against zero, which is what this pins.
	calls := make([]appwire.PendingAskCall, 64)
	for i := range calls {
		calls[i] = appwire.PendingAskCall{TurnID: "turn", CallID: strings.Repeat("q", 64)}
	}
	item := appwire.ThreadItem{
		Type: "userMessage",
		ID:   "answer",
		Text: "kept",
		Observation: &appwire.SessionObservationMetadata{
			TurnID:                 "answer-turn",
			AnswerToTurnIDs:        []string{"turn"},
			ResolvedAskCalls:       calls,
			ResolvedAskLegacyCount: 2,
		},
	}
	omitted, changed := OmitResolvedAskCalls(item, 0)
	if !changed {
		t.Fatal("zero limit did not omit the call list")
	}
	observation := omitted.Observation
	if observation == nil {
		t.Fatal("omission dropped the whole observation")
	}
	if len(observation.ResolvedAskCalls) != 0 {
		t.Fatalf("omitted item kept %d call-list entries", len(observation.ResolvedAskCalls))
	}
	if observation.ResolvedAskLegacyCount != 2 {
		t.Fatalf("legacy count = %d, want 2: omission must keep the count", observation.ResolvedAskLegacyCount)
	}
	if got, want := observation.AnswerToTurnIDs, []string{"turn"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("turn links = %v, want %v", got, want)
	}
	if omitted.Text != item.Text {
		t.Fatalf("omission changed the body: %q, want the supplied %q", omitted.Text, item.Text)
	}
	// The input item keeps its list: bounding a read must not rewrite the
	// canonical item another read serves.
	if item.Observation == observation {
		t.Fatal("omission edited the caller's observation instead of copying it")
	}
	if got := len(item.Observation.ResolvedAskCalls); got != len(calls) {
		t.Fatalf("canonical item lost its call list: %d of %d", got, len(calls))
	}
}

// TestOmitResolvedAskCallsMeasuresUnderAPositiveLimit pins the other half: a
// list at or under the limit is left alone, so a page that already fits keeps
// its evidence.
func TestOmitResolvedAskCallsMeasuresUnderAPositiveLimit(t *testing.T) {
	t.Parallel()
	calls := []appwire.PendingAskCall{{TurnID: "turn", CallID: "call"}}
	item := appwire.ThreadItem{
		Type:        "userMessage",
		ID:          "answer",
		Observation: &appwire.SessionObservationMetadata{ResolvedAskCalls: calls},
	}
	if omitted, changed := OmitResolvedAskCalls(item, 8<<10); changed {
		t.Fatalf("a list under the limit was omitted: %+v", omitted.Observation.ResolvedAskCalls)
	}
}
