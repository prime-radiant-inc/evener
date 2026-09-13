package hub

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// watchListForCap builds n valid watch rows; the first inertCount are inactive.
func watchListForCap(n, inertCount int) []appwire.EvenerWatchInfo {
	watches := make([]appwire.EvenerWatchInfo, 0, n)
	for i := range n {
		watches = append(watches, appwire.EvenerWatchInfo{
			ID:        fmt.Sprintf("watch-%03d", i),
			Source:    "self",
			CreatedAt: "2026-09-12T10:00:00Z",
			Active:    i >= inertCount,
		})
	}
	return watches
}

func projectSessionWithWatches(t *testing.T, watches []appwire.EvenerWatchInfo) hubapi.NavigationSessionSummary {
	t.Helper()
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Kind: "session", State: "idle", Watches: watches,
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	if len(resource.Current.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one", resource.Current.Sessions)
	}
	return resource.Current.Sessions[0]
}

// A session with more watches than the cap keeps the armed rows and reports the
// exact count it dropped, so the rail's armed count can never quietly lie.
func TestNavigationWatchesCapKeepsArmedAndCountsOmittedExact(t *testing.T) {
	const inert = 12
	row := projectSessionWithWatches(t, watchListForCap(40, inert))
	if len(row.Watches) != maxNavigationWatches {
		t.Fatalf("kept watches = %d, want the cap %d", len(row.Watches), maxNavigationWatches)
	}
	if row.OmittedWatches != 40-maxNavigationWatches {
		t.Fatalf("OmittedWatches = %d, want %d", row.OmittedWatches, 40-maxNavigationWatches)
	}
	armedKept := 0
	for _, watch := range row.Watches {
		if watch.Active {
			armedKept++
		}
	}
	if armedKept != 40-inert {
		t.Fatalf("armed watches kept = %d, want all %d armed rows to survive the cap", armedKept, 40-inert)
	}
}

// A session at the cap carries no omitted count, and the count stays absent
// below it. This is the boundary the "+N" wording keys on.
func TestNavigationWatchesAtOrUnderCapCarriesNoOmitted(t *testing.T) {
	for _, n := range []int{1, maxNavigationWatches - 1, maxNavigationWatches} {
		row := projectSessionWithWatches(t, watchListForCap(n, 0))
		if len(row.Watches) != n {
			t.Fatalf("n=%d: kept watches = %d, want %d", n, len(row.Watches), n)
		}
		if row.OmittedWatches != 0 {
			t.Fatalf("n=%d: OmittedWatches = %d, want 0", n, row.OmittedWatches)
		}
	}
}

// A row the projector cannot represent (an invalid created_at the codec would
// reject) is dropped; the omitted count must include it so no layer drops a row
// without counting it.
func TestNavigationWatchesCountsUnrepresentableDrops(t *testing.T) {
	watches := watchListForCap(maxNavigationWatches, 0)
	watches[0].CreatedAt = "not-a-timestamp"
	row := projectSessionWithWatches(t, watches)
	if len(row.Watches) != maxNavigationWatches-1 {
		t.Fatalf("kept watches = %d, want %d", len(row.Watches), maxNavigationWatches-1)
	}
	if row.OmittedWatches != 1 {
		t.Fatalf("OmittedWatches = %d, want 1 for the dropped unrepresentable row", row.OmittedWatches)
	}
}

// The hub's byte-budget fitter sheds whole watch rows when a session overflows.
// That drop must add to the omitted count, or the UI would silently undercount.
func TestNavigationWatchTrimAddsOmittedCount(t *testing.T) {
	rows := []hubapi.NavigationSessionSummary{{
		Watches: hubapi.NavigationArray[hubapi.NavigationWatchSummary]{
			{ID: "a", Source: "self", CreatedAt: "2026-09-12T10:00:00Z"},
			{ID: "b", Source: "self", CreatedAt: "2026-09-12T10:00:00Z"},
			{ID: "c", Source: "self", CreatedAt: "2026-09-12T10:00:00Z"},
		},
		OmittedWatches: 5,
	}}
	trimNavigationWatchPayloads(rows, navigationWatchPayloadNoWatches)
	if rows[0].Watches != nil {
		t.Fatalf("trim left watches = %+v, want nil", rows[0].Watches)
	}
	if rows[0].OmittedWatches != 8 {
		t.Fatalf("OmittedWatches = %d, want 5 pre-existing + 3 trimmed = 8", rows[0].OmittedWatches)
	}
	// Dropping delivery instants removes no row, so the count must not move.
	rows[0].Watches = hubapi.NavigationArray[hubapi.NavigationWatchSummary]{{ID: "a", Source: "self", CreatedAt: "2026-09-12T10:00:00Z"}}
	trimNavigationWatchPayloads(rows, navigationWatchPayloadNoDeliveryTimes)
	if rows[0].OmittedWatches != 8 {
		t.Fatalf("OmittedWatches changed on a non-dropping trim: %d, want 8", rows[0].OmittedWatches)
	}
}

// cloneNavigationSummary must copy the count, or a fitted clone would lose it.
func TestCloneNavigationSummaryCopiesOmittedWatches(t *testing.T) {
	original := hubapi.NavigationSessionSummary{OmittedWatches: 7}
	clone := cloneNavigationSummary(original)
	if clone.OmittedWatches != 7 {
		t.Fatalf("clone OmittedWatches = %d, want 7", clone.OmittedWatches)
	}
}
