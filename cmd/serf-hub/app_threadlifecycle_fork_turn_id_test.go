package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestParseSourceTurnIDRejectsIDsThatAreNotTranscriptEntryIndexes (kata rk09)
// pins the stance thread/fork takes on a turn id that is not an entry index.
//
// parseSourceTurnID's result is handed straight to agent.ForkSessionAtUserTurn
// as a divergence position — a 1-based index into the parent transcript's
// entries. Only ids the transcript's own entry-index numbering produced mean
// anything there. A reserved client-mutation id (appwire.ClientMutationTurnID)
// and the synthetic prelude id are numbered by something else entirely, so
// there is no entry they name; refusing is the only answer that cannot cut a
// child session at a position the user never pointed at.
//
// The refusal today falls out of the numeric parse failing. Do not "fix" a
// failing fork by making this lenient — stripping the reserved namespace's
// marker would resolve turn_m7 to entry 7, which is an unrelated entry in
// every session where the two counters have diverged (which is all of them
// past the first turn or two).
func TestParseSourceTurnIDRejectsIDsThatAreNotTranscriptEntryIndexes(t *testing.T) {
	for _, raw := range []string{
		appwire.ClientMutationTurnID(1),
		appwire.ClientMutationTurnID(7),
		appwire.SystemPreludeTurnID,
	} {
		turn, err := parseSourceTurnID(raw)
		if err == nil {
			t.Fatalf("parseSourceTurnID(%q) = %d with no error; it names no transcript entry, so forking on it cuts the child at an unrelated position", raw, turn)
		}
		if !strings.Contains(err.Error(), "sourceTurnId") {
			t.Fatalf("parseSourceTurnID(%q) error = %q, want it to name the offending parameter", raw, err)
		}
	}

	// The ids the transcript's entry-index numbering does produce still parse,
	// in both the prefixed and bare forms the wire allows.
	for _, tc := range []struct {
		raw  string
		want int
	}{{"turn_4", 4}, {"4", 4}} {
		turn, err := parseSourceTurnID(tc.raw)
		if err != nil || turn != tc.want {
			t.Fatalf("parseSourceTurnID(%q) = (%d, %v), want (%d, nil)", tc.raw, turn, err, tc.want)
		}
	}
}
