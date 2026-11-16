package transcriptindex

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// TestResolveTurnWindowGroupsANonAdjacentlyReopenedTurn pins the turn-addressed
// read against a turn whose items are not physically adjacent: a reopen under an
// already-closed id lands its later fragment after another turn's items, so the
// window's item span must cover the reopened turn's last entry rather than its
// first run's neighbourhood. The whole-file projection is the oracle.
func TestResolveTurnWindowGroupsANonAdjacentlyReopenedTurn(t *testing.T) {
	path := writeFixture(t, fixture{header: transcript.Header{SessionID: "th_turn_window_reopen"}, lines: []fixtureLine{
		entryLine(opens("turn_ra", schema.TurnSpanExecution, user("first attempt"))),
		entryLine(inTurn("turn_ra", assistant(text("first try")))),
		entryLine(completion("turn_ra", schema.TurnFailed, 1, 10)),
		entryLine(opens("turn_rb", schema.TurnSpanExecution, user("unrelated turn"))),
		entryLine(inTurn("turn_rb", assistant(text("rb's own answer")))),
		entryLine(completion("turn_rb", schema.TurnCompleted, 2, 10)),
		entryLine(reopen("turn_ra")),
		entryLine(inTurn("turn_ra", assistant(text("second try")))),
		entryLine(completion("turn_ra", schema.TurnCompleted, 3, 20)),
	}})
	x := openIndex(t, path, t.TempDir())
	t.Cleanup(func() { _ = x.Close() })

	for _, tc := range []struct {
		name     string
		selector appwire.ThreadTurnWindow
		wantIDs  []string
		want     []string
	}{
		{
			name:     "the reopened turn returns both fragments in order",
			selector: appwire.ThreadTurnWindow{Turn: "turn_ra"},
			wantIDs:  []string{"turn_ra"},
			want:     []string{"first attempt", "first try", "second try"},
		},
		{
			name:     "the intervening turn returns only its own items",
			selector: appwire.ThreadTurnWindow{Turn: "turn_rb"},
			wantIDs:  []string{"turn_rb"},
			want:     []string{"unrelated turn", "rb's own answer"},
		},
		{
			name:     "a latest window spanning both returns every item once",
			selector: appwire.ThreadTurnWindow{Latest: 2},
			wantIDs:  []string{"turn_ra", "turn_rb"},
			want:     []string{"first attempt", "first try", "unrelated turn", "rb's own answer", "second try"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := x.ResolveTurnWindow(tc.selector)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got.TurnIDs) != fmt.Sprint(tc.wantIDs) {
				t.Fatalf("turn ids = %v, want %v", got.TurnIDs, tc.wantIDs)
			}
			texts := make([]string, len(got.Candidates))
			for i := range got.Candidates {
				texts[i] = got.Candidates[i].Item.Text
			}
			if fmt.Sprint(texts) != fmt.Sprint(tc.want) {
				t.Fatalf("candidate texts = %v, want %v", texts, tc.want)
			}
		})
	}
}
