package appprojector

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestMergeAppwireDelegateInfoNewerActivityInPrimary covers line 1081:
// the primary branch (incoming has higher revision) where
// current.LatestActivityAt is newer than merged (incoming) activity.
func TestMergeAppwireDelegateInfoNewerActivityInPrimary(t *testing.T) {
	current := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 1,
		LatestActivityAt:   "2024-01-03T00:00:00Z",
	}
	incoming := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 2,
		LatestActivityAt:   "2024-01-01T00:00:00Z",
	}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("higher revision should trigger merge")
	}
	if merged.LatestActivityAt != "2024-01-03T00:00:00Z" {
		t.Fatalf("should keep current's newer activity, got %q", merged.LatestActivityAt)
	}
}
