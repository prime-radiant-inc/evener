package scratch

import (
	"strings"
	"testing"
)

// TestReclaimOwnReadDirError covers the ReadDir error path in reclaimOwn
// (lines 135-137) by passing a base directory that doesn't exist.
func TestReclaimOwnReadDirError(t *testing.T) {
	var sb strings.Builder
	reclaimOwn("/nonexistent-scratch-base-for-test", "test-prefix", &sb)
	out := sb.String()
	if !strings.Contains(out, "not reclaiming") {
		t.Fatalf("reclaimOwn with nonexistent base should warn 'not reclaiming', got %q", out)
	}
}
