package hub

import (
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"

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

// Own-session counts do not depend on watch detail validity or a row cap.
func TestNavigationOwnWatchCounts(t *testing.T) {
	for _, n := range []int{0, 1, 32, 40, 2000} {
		watches := watchListForCap(n, n/3)
		if n > 0 {
			watches[0].CreatedAt = "invalid"
			watches[0].ID = ""
		}
		row := projectSessionWithWatches(t, watches)
		if row.WatchCount != n || row.ArmedWatchCount != n-n/3 {
			t.Fatalf("n=%d counts=%d/%d", n, row.WatchCount, row.ArmedWatchCount)
		}
		if !navigationSessionValueValid(row) {
			t.Fatal("compact counts poisoned by detail")
		}
	}
}

func TestNavigationLocationCountsSelectedSessionOnly(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	metas := []schema.SessionMeta{
		{ID: "original", CreatedAt: now, UpdatedAt: now, ForkLabel: "before edit"},
		{ID: "parent", CreatedAt: now, UpdatedAt: now, ParentSessionID: "original"},
	}
	live := []hubcore.LiveEntry{
		{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle},
		{PID: 2, SessionID: "original", Status: appwire.ThreadStatusIdle, Watches: watchListForCap(40, 12)},
	}
	tree := hubcore.BuildTreeAt(metas, live, nil, now)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "g", Tree: tree})
	if err != nil {
		t.Fatal(err)
	}
	rows := projection.LivePage(0, 50).Sessions
	if len(rows) != 1 || rows[0].SessionID != "parent" || rows[0].WatchCount != 0 || len(rows[0].Children) != 0 {
		t.Fatalf("global rows=%+v", rows)
	}
	location, ok := projection.Location("local:original")
	if !ok || location.Session == nil || location.TopLevel || location.TopLevelRef != "local:parent" {
		t.Fatalf("location=%+v found=%v", location, ok)
	}
	if location.Session.WatchCount != 40 || location.Session.ArmedWatchCount != 28 || len(location.Session.Children) != 0 {
		t.Fatalf("selected session=%+v", location.Session)
	}
}
