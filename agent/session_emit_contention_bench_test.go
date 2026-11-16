package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
)

// The emit path takes the session mutex on every event to read the causal
// provenance stamp (activeCausalProvenance), and ContextStatus takes the same
// mutex to copy history. These benchmarks measure that shared lock under
// concurrency, the only shape in which the cost of taking it is visible: a
// single-goroutine loop leaves it uncontended, where every lock shape costs the
// same.
//
// Every worker runs the identical loop, so all of them consume b.RunParallel's
// shared iteration budget at the same rate. Splitting workers into emitter and
// sampler roles instead would let the much faster emitter starve the sampler of
// iterations, and the reported average would then describe the emitter alone.
//
// History size is pinned per sub-benchmark because ContextStatus's copy is
// O(history) while the emitter's own work is O(1): a large history makes the
// sample's own CPU dominate the wall clock and masks the lock.
//
// Nothing consumes the event channel, and that is deliberate. The subject is the
// emitter's own work; a draining consumer would add its own session-mutex reads
// and its own backpressure to every measured operation, and both would be
// charged to the lock under test.

const (
	emitContentionHistorySmall = 32
	emitContentionHistoryLarge = 1000
	// emitContentionEmitsPerSample is how many emits each worker completes
	// between its own ContextStatus samples. A round emits on the order of 10^4
	// events while a status sample is taken a handful of times, so one sample
	// per 2^6 emits keeps both sides continuously contending without letting the
	// sample's own O(history) CPU swamp the measurement.
	emitContentionEmitsPerSample = 64
)

// newEmitContentionFixture builds the measured session outside any timed region.
// It reuses the ContextStatus fixture for history and the context manager, and
// adds the two fields emit reads.
func newEmitContentionFixture(turns int) *Session {
	s, _ := newContextStatusLargeFixture(turns)
	s.id = "bench-emit-contention"
	s.events = make(chan events.SessionEvent, 256)
	return s
}

func BenchmarkEmitContention(b *testing.B) {
	payload := events.AssistantTextDeltaData{Delta: "x"}
	governors := []struct {
		name  string
		turns int
	}{
		{name: "history32", turns: emitContentionHistorySmall},
		{name: "history1000", turns: emitContentionHistoryLarge},
	}

	// emit-only: every measured operation takes the session mutex while every
	// other emitter takes it too.
	for _, gov := range governors {
		b.Run("emit-only/"+gov.name, func(b *testing.B) {
			s := newEmitContentionFixture(gov.turns)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					s.emit(payload)
				}
			})
		})
	}

	// sample-only: ContextStatus contending with itself, the other half of the
	// same lock.
	for _, gov := range governors {
		b.Run("sample-only/"+gov.name, func(b *testing.B) {
			s := newEmitContentionFixture(gov.turns)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_, _ = s.ContextStatus()
				}
			})
		})
	}

	// emit-with-sampling: one emit per iteration and one ContextStatus every
	// emitContentionEmitsPerSample, in every worker — the daemon's shape, where a
	// status sample lands while the round keeps emitting.
	for _, gov := range governors {
		b.Run("emit-with-sampling/"+gov.name, func(b *testing.B) {
			s := newEmitContentionFixture(gov.turns)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				sinceSample := 0
				for pb.Next() {
					s.emit(payload)
					if sinceSample++; sinceSample == emitContentionEmitsPerSample {
						sinceSample = 0
						_, _ = s.ContextStatus()
					}
				}
			})
		})
	}
}
