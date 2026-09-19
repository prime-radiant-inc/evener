#!/bin/sh
# Verifier for smoke-feature: success is defined by this probe test, not by
# a test the agent can read (the paper's verifier-driven pattern).
set -e
cd "$1"
cat > stats/probe_verify_test.go <<'EOF'
package stats

import "testing"

func TestProbeMedian(t *testing.T) {
	if Median([]float64{3, 1, 2}) != 2 {
		t.Fatalf("Median([3,1,2]) = %v, want 2", Median([]float64{3, 1, 2}))
	}
	if Median([]float64{4, 1, 3, 2}) != 2.5 {
		t.Fatalf("Median([4,1,3,2]) = %v, want 2.5", Median([]float64{4, 1, 3, 2}))
	}
	if Median(nil) != 0 {
		t.Fatal("Median(nil) must be 0")
	}
}
EOF
exec go test ./...
