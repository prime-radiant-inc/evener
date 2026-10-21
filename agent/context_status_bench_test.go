package agent

import "testing"

// sampleContextStatus is the only part of this benchmark that differs between
// the pinned baseline and the candidate.
func sampleContextStatus(s *Session) {
	_, _ = s.ContextStatus()
}

func BenchmarkContextStatusSampling(b *testing.B) {
	benchmarkContextStatusSampling(b, sampleContextStatus)
}
