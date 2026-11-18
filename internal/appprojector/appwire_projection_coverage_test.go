package appprojector

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestDelegateActivityAfterEmptyCandidate(t *testing.T) {
	if delegateActivityAfter("", "2024-01-01T00:00:00Z") {
		t.Fatal("empty candidate should return false")
	}
}

func TestDelegateActivityAfterEmptyCurrent(t *testing.T) {
	if !delegateActivityAfter("2024-01-01T00:00:00Z", "") {
		t.Fatal("non-empty candidate with empty current should return true")
	}
}

func TestDelegateActivityAfterNewerCandidate(t *testing.T) {
	if !delegateActivityAfter("2024-01-02T00:00:00Z", "2024-01-01T00:00:00Z") {
		t.Fatal("newer candidate should return true")
	}
}

func TestDelegateActivityAfterOlderCandidate(t *testing.T) {
	if delegateActivityAfter("2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z") {
		t.Fatal("older candidate should return false")
	}
}

func TestDelegateActivityAfterInvalidTimestamp(t *testing.T) {
	if delegateActivityAfter("not-a-timestamp", "2024-01-01T00:00:00Z") {
		t.Fatal("invalid candidate should return false")
	}
	if delegateActivityAfter("2024-01-01T00:00:00Z", "not-a-timestamp") {
		t.Fatal("invalid current should return false")
	}
}

func TestDelegateActivityAfterSameTimestamp(t *testing.T) {
	ts := "2024-01-01T00:00:00Z"
	if delegateActivityAfter(ts, ts) {
		t.Fatal("same timestamp should return false (not strictly after)")
	}
}

func TestMergeAppwireDelegateInfoNewIncoming(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 2, Status: "running"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("higher projection revision should trigger merge")
	}
	if merged.ProjectionRevision != 2 || merged.Status != "running" {
		t.Fatal("should merge incoming fields")
	}
}

func TestMergeAppwireDelegateInfoSameRevisionNewerActivity(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-02T00:00:00Z"}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-03T00:00:00Z"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("newer activity at same revision should trigger update")
	}
	if merged.LatestActivityAt != "2024-01-03T00:00:00Z" {
		t.Fatalf("should use newer activity timestamp, got %q", merged.LatestActivityAt)
	}
}

func TestMergeAppwireDelegateInfoNoChange(t *testing.T) {
	current := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-02T00:00:00Z"}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1, LatestActivityAt: "2024-01-01T00:00:00Z"}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if changed {
		t.Fatal("older activity at same revision should not trigger update")
	}
	if merged.LatestActivityAt != "2024-01-02T00:00:00Z" {
		t.Fatal("should keep current activity timestamp")
	}
}

func TestMergeAppwireDelegateInfoEmptyCurrent(t *testing.T) {
	current := appwire.EvenerDelegateInfo{}
	incoming := appwire.EvenerDelegateInfo{DelegateID: "dlg_1", ProjectionRevision: 1}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("empty current DelegateID should trigger merge")
	}
	if merged.DelegateID != "dlg_1" {
		t.Fatal("should use incoming")
	}
}

// The drain hold is its own facet, outside the projection-revision ladder:
// entering or leaving the drain appends no durable delegate event and moves no
// durable field, and the hold freezes LatestActivityAt for its whole span, so
// both edges arrive at the revision already recorded. Before this the merge
// returned changed=false and the projector dropped the frame, which is exactly
// the update a loaded row needs.
func TestMergeAppwireDelegateInfoHoldAtUnchangedRevision(t *testing.T) {
	current := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 4,
		Status:             "running",
		LatestActivityAt:   "2024-01-01T00:00:00Z",
	}
	held := appwire.EvenerDelegateInfo{
		DelegateID:                "dlg_1",
		ProjectionRevision:        4,
		Status:                    "running",
		LatestActivityAt:          "2024-01-01T00:00:00Z",
		DrainHeldByJobs:           true,
		RunningJobCount:           1,
		OldestRunningJobStartedAt: "2024-01-01T00:00:00Z",
	}
	merged, changed := mergeAppwireDelegateInfo(current, held)
	if !changed {
		t.Fatal("the hold at an unchanged revision was dropped; a loaded row would never learn the wait")
	}
	if !merged.DrainHeldByJobs || merged.RunningJobCount != 1 || merged.OldestRunningJobStartedAt == "" {
		t.Fatalf("merged hold = %v, count = %d, oldest = %q; want the frame's hold",
			merged.DrainHeldByJobs, merged.RunningJobCount, merged.OldestRunningJobStartedAt)
	}

	// The release edge, at the same revision again: the row must stop
	// claiming the wait rather than keeping the last hold forever.
	released, changed := mergeAppwireDelegateInfo(merged, current)
	if !changed {
		t.Fatal("the release at an unchanged revision was dropped; the row would keep claiming the wait")
	}
	if released.DrainHeldByJobs || released.RunningJobCount != 0 || released.OldestRunningJobStartedAt != "" {
		t.Fatalf("released hold = %v, count = %d, oldest = %q; want all three cleared",
			released.DrainHeldByJobs, released.RunningJobCount, released.OldestRunningJobStartedAt)
	}

	// A frame that repeats the hold states nothing new.
	if _, changed := mergeAppwireDelegateInfo(held, held); changed {
		t.Fatal("a repeated hold frame published an update")
	}
}
