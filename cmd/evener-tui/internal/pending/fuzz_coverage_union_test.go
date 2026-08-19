//go:build evenerfuzz

package pending

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestPendingReconcileOrdering(t)
		TestPendingReferenceMatchingAndRealClock(t)
	}
}
