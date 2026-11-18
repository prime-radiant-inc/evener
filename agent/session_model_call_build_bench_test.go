package agent

// Benchmark for the per-round request build (#6442). prepareModelRequestWithError
// used to build the primary request and, unconditionally, an untrimmed fallback
// request that most rounds never dispatch: every round paid for a message slice
// over the projected history and another over the unprojected one. The fallback
// now lays its own messages out from the unprojected source when a fallback
// actually runs, so a round that never falls back builds one request.
//
// Both sub-benchmarks are built from buildModelRequest alone, so they compile
// and measure identically on the pre-fix and post-fix trees: "one" is a single
// build, "pair" is the eager pair a round used to pay. The round-level result
// is read from BenchmarkPrepareModelRequestHistory, which drives one real
// preparation and therefore includes the removed build on the pre-fix tree.

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// requestBuildBenchTurns is the history size the audit's envelope is quoted at.
const requestBuildBenchTurns = 1024

// newRequestBuildBenchSession returns a session whose history holds the
// benchmark's turns, and returns that history as one request expands it.
func newRequestBuildBenchSession(tb testing.TB) (*Session, []llm.Message) {
	tb.Helper()
	s := newRequestHistoryPreparationSession(tb, requestBuildBenchTurns, requestHistoryNoFoldLane)
	s.mu.Lock()
	turns := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()
	return s, expandHistory(turns, replayScope{})
}

// BenchmarkBuildModelRequest measures one build against a 1,024-message
// history, and the pair the round used to pay before the fallback build moved
// behind the fallback chain.
func BenchmarkBuildModelRequest(b *testing.B) {
	s, history := newRequestBuildBenchSession(b)
	profile := s.currentProfile()
	b.Run("one", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			req := s.buildModelRequest(profile, "system prompt", history, nil, "", 0)
			if len(req.Messages) != len(history)+1 {
				b.Fatalf("built %d messages, want %d", len(req.Messages), len(history)+1)
			}
		}
	})
	b.Run("pair", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			primary := s.buildModelRequest(profile, "system prompt", history, nil, "", 0)
			fallback := s.buildModelRequest(profile, "system prompt", history, nil, "", 0)
			if len(primary.Messages) != len(fallback.Messages) {
				b.Fatalf("primary and fallback message counts differ: %d and %d", len(primary.Messages), len(fallback.Messages))
			}
		}
	})
}
