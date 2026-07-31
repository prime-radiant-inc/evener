package transcript

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestTurnIndexFromIDOnlyResolvesTranscriptEntryIndexes (kata rk09) pins the
// zero the TUI's fork draft depends on.
//
// hub_browse.go's startForkDraft sends ChatMessage.TurnIndex to thread/fork as
// a divergence position — a 1-based index into the parent transcript — and
// refuses when it is <= 0 ("fork requires persisted transcript turn
// identity"). A turn id minted by something other than the transcript's
// entry-index numbering names no such position, so parsing must yield 0 and
// let that guard fire. Teaching this to read the reserved client-mutation
// namespace as a number would turn turn_m7 into entry 7, an unrelated entry in
// every session where the mutation counter and the entry index have diverged.
func TestTurnIndexFromIDOnlyResolvesTranscriptEntryIndexes(t *testing.T) {
	for _, raw := range []string{
		appwire.ClientMutationTurnID(1),
		appwire.ClientMutationTurnID(7),
		appwire.SystemPreludeTurnID,
		"",
	} {
		if got := TurnIndexFromID(raw); got != 0 {
			t.Fatalf("TurnIndexFromID(%q) = %d, want 0 — it names no transcript entry, and a nonzero answer makes the fork draft cut at that entry", raw, got)
		}
	}

	for _, tc := range []struct {
		raw  string
		want int
	}{{"turn_1", 1}, {"turn_42", 42}} {
		if got := TurnIndexFromID(tc.raw); got != tc.want {
			t.Fatalf("TurnIndexFromID(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}
