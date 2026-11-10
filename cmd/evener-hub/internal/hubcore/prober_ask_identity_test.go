package hubcore

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// collectStrings flattens any structure into the string values it carries, so
// a test can prove whether a sentinel identity crosses a plumbing boundary
// without naming (or depending on) the field that will eventually carry it.
func collectStrings(v any) []string {
	var out []string
	blob, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	// Walk the raw decoder so string values inside maps and slices are seen
	// without depending on field tags.
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, item := range t {
				walk(item)
			}
		case map[string]any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	var decoded any
	if json.Unmarshal(blob, &decoded) != nil {
		return nil
	}
	walk(decoded)
	return out
}

// TestProbeAndRosterRowCarryAskIdentityForTheExactTurnFetch is the round-7
// R2/S9 red: the composite identity is missing end to end. The thread row
// already carries the pending ask's exact per-call identity —
// EvenerThread.PendingAsk is the full PendingAskState, whose calls carry
// TurnID and CallID as SEPARATE fields (appwire/types_pending_ask.go) — but the
// probe that feeds the roster row (ProbeResultFromThread) fills only the
// ask flag and the bounded question text, so neither id ever reaches
// LiveEntryThreadFacts' row, the attention entry, or the needs_you frame
// (whose payload is flags-only, agent/job_watch_hub.go). Spec 4's declared
// payload names only the displayed first question's CALL id while its fetch
// clause needs the ask TURN id, and no call-id-to-turn-id resolver exists — an
// implementer building from the payload lists ships a frame that cannot
// address its own exact-turn fetch.
//
// RED today: neither id crosses. The promised contract: the composite
// (turnId, callId) travels ProbeResult → LiveEntry → the roster projection →
// the frame, so the exact-turn fetch {turn: askTurnId} is addressable from a
// frame alone.
func TestProbeAndRosterRowCarryAskIdentityForTheExactTurnFetch(t *testing.T) {
	t.Parallel()
	const askTurnID = "ask-turn-3f9c17d2"
	const askCallID = "ask-call-77c104"
	root := appwire.Thread{
		ID: "sess-probe-1",
		Evener: appwire.EvenerThread{
			AskPending: true,
			PendingQuestion: &appwire.PendingQuestion{
				Question: "Which datastore for the ingest path?",
				Options:  []string{"Postgres", "SQLite"},
				Count:    1,
			},
			PendingAsk: &appwire.PendingAskState{
				Version:    1,
				InstanceID: "sess-probe-1",
				Revision:   4,
				Calls:      []appwire.PendingAskCall{{TurnID: askTurnID, CallID: askCallID}},
			},
		},
	}

	// Anti-regression: the row's existing ask facts do cross.
	result := ProbeResultFromThread(root)
	if !result.PendingAsk || result.PendingQuestion == nil {
		t.Fatalf("probe lost the row's existing ask facts: %+v", result)
	}
	entry := LiveEntryThreadFacts(LiveEntry{SessionID: "sess-probe-1"}, result)
	if !entry.PendingAsk || entry.PendingQuestion == nil {
		t.Fatalf("roster row lost the probe's existing ask facts: %+v", entry)
	}

	// RED: the composite identity must reach the roster row the needs_you
	// frame derives from. Neither id crosses today.
	values := collectStrings(entry)
	if !slices.Contains(values, askTurnID) || !slices.Contains(values, askCallID) {
		t.Fatalf("the roster row the needs_you frame derives from carries no ask identity: ask turn id %q and call id %q are both absent from the row the probe built (excerpt count %d), so the frame cannot address the exact-turn fetch",
			askTurnID, askCallID, entry.PendingQuestion.Count)
	}
	if !strings.Contains(strings.Join(values, "\n"), "Which datastore for the ingest path?") {
		t.Fatal("sanity: the row's question text also failed to cross — the fixture is broken")
	}
}
