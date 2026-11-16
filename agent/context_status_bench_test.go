package agent

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// sampleContextStatus is the only part of this benchmark that differs between
// the pinned baseline and the candidate.
func sampleContextStatus(s *Session) {
	_, _ = s.ContextStatus()
}

func BenchmarkContextStatusSampling(b *testing.B) {
	benchmarkContextStatusSampling(b, sampleContextStatus)
}

// BenchmarkContextStatusSamplingAfterAppend measures the sample the daemon
// actually takes: one per event that announces a finished round, by which time
// the round has appended its turn. The shipped benchmark samples an unchanged
// history, where the memo answers with no copy at all, so it cannot see the
// copy an append forces; this one appends between samples and reports the
// copy that costs.
func BenchmarkContextStatusSamplingAfterAppend(b *testing.B) {
	for _, turns := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("%dturns", turns), func(b *testing.B) {
			s, _ := newContextStatusLargeFixture(turns)
			turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(contextStatusText()))
			// Grow to the final length outside the timed region, so the
			// append's own reallocation is not charged to the sample.
			s.mu.Lock()
			grown := make([]schema.Turn, len(s.history), len(s.history)+b.N)
			copy(grown, s.history)
			s.history = grown
			s.mu.Unlock()
			_, _ = s.ContextStatus() // the previous round's sample
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				s.mu.Lock()
				s.history = append(s.history, turn)
				s.mu.Unlock()
				sampleContextStatus(s)
			}
		})
	}
}
