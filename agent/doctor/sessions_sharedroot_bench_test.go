package doctor

import (
	"testing"
)

// BenchmarkListSessionsSharedRoot measures the sweep over one root plus 18
// children sharing that root's delegates journal — the shape measured on a
// real state root where the pre-cache code re-folded the 1.7MB journal 18
// times (~10.6s for 50 sessions). The cache must collapse the shared-root
// fold to one; this benchmark documents the per-sweep cost so a regression
// (per-child refold) shows up as a step change in ns/op.
func BenchmarkListSessionsSharedRoot(b *testing.B) {
	const children = 18
	base, _ := sharedRootFixtureTB(b, children)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ListSessions(base, SessionsOpts{}); err != nil {
			b.Fatal(err)
		}
	}
}
