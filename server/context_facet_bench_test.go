package server

import "testing"

// BenchmarkContextFacetPublication measures the bridge publication path, not
// the session sample: it calls srv.refreshFacets(facetContext) on the same
// real-session fixture as TestContextFacetAllocationBudget. The agent
// sample benchmark measures session scaling; neither number is the other's
// cost.
func BenchmarkContextFacetPublication(b *testing.B) {
	fixture := newContextFacetFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fixture.srv.refreshFacets(facetContext)
	}
}
