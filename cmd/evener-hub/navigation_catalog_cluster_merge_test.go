package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A merged duplicate-Key project must reconcile its groups' synthetic cluster
// rows, not merely concatenate them.
//
// hubcore mints a cluster's id as a function of the owning project's display
// name and the folded title (hubcore tree.go clusterID), and an unresolved
// group's display name is the basename of its working directory (hubcore tree.go
// "name := ...filepath.Base(displayPath)"). Two unresolved directories that share
// a basename therefore mint the *same* cluster id when their sessions repeat one
// title, and the merge folds those two groups into one project row. Concatenating
// both groups' tiers leaves two rows carrying that one id, and the id is the
// entity key the client addresses the row by (navigationNodeRef -> the summary
// Ref -> the normalized session entity key). Two rows with one id make the
// merged project's normalized graph carry two session entities with one key and
// fail validation with "duplicate navigation entity key" - the same "graph"
// failure the merge exists to prevent, relocated from the catalog page to the
// project detail.
//
// The reachability premise is asserted, not assumed: the test builds the tree
// through hubcore from session metadata, so the colliding ids are the ones the
// real derivation mints, and it fails if that derivation stops producing a
// collision.
func TestNavigationCatalogGraphMergesCollidingClusterRows(t *testing.T) {
	generation := "00112233445566778899aabbccddeeff"
	now := time.Unix(1_700_000_000, 0).UTC()
	const repeatedTitle = "describe this image"
	meta := func(id, name, workingDir string, updated time.Time) schema.SessionMeta {
		return schema.SessionMeta{
			ID:        id,
			Name:      name,
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: workingDir},
			CreatedAt: updated,
			UpdatedAt: updated,
		}
	}

	// Colliding clusters in one tier: both directories are host-side paths the
	// controller cannot resolve, both end in "shared" so both groups are named
	// "shared", and each group's three same-titled idle runs fold into a cluster.
	// The two clusters share one id and both classify Current (all members are
	// well inside the archive window), so the merged Current tier would hold two
	// rows with that one id.
	t.Run("one tier", func(t *testing.T) {
		metas := []schema.SessionMeta{
			meta("01ARZ3NDEKTSV4RRFFQ69G5FAA", repeatedTitle, "/host-a/shared", now.Add(-3*time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FAB", repeatedTitle, "/host-a/shared", now.Add(-4*time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FAC", repeatedTitle, "/host-a/shared", now.Add(-5*time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FBA", repeatedTitle, "/host-b/shared", now.Add(-13*time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FBB", repeatedTitle, "/host-b/shared", now.Add(-14*time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FBC", repeatedTitle, "/host-b/shared", now.Add(-15*time.Minute)),
		}
		navigationAssertMergedClusterGraph(t, generation, now, metas, 6, 6)
	})

	// Colliding clusters in different tiers: host-a's repeated runs are older
	// than the archive window so its cluster classifies Archived, while one
	// recent run under the same directory keeps host-a's project active (and so
	// in the same catalog bucket as host-b, which is what lets the merge see
	// both groups at all). host-b's repeated runs are recent, so its cluster is
	// Current. The project resource normalizes Current, Recent, and Archived into
	// ONE graph under one resource key, so the two occurrences collide even
	// though the per-tier merge never puts them in the same slice.
	t.Run("different tiers", func(t *testing.T) {
		metas := []schema.SessionMeta{
			meta("01ARZ3NDEKTSV4RRFFQ69G5FAA", repeatedTitle, "/host-a/shared", now.Add(-30*24*time.Hour)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FAB", repeatedTitle, "/host-a/shared", now.Add(-30*24*time.Hour-time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FAC", repeatedTitle, "/host-a/shared", now.Add(-30*24*time.Hour-2*time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FAD", "keep this project active", "/host-a/shared", now.Add(-time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FBA", repeatedTitle, "/host-b/shared", now.Add(-13*time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FBB", repeatedTitle, "/host-b/shared", now.Add(-14*time.Minute)),
			meta("01ARZ3NDEKTSV4RRFFQ69G5FBC", repeatedTitle, "/host-b/shared", now.Add(-15*time.Minute)),
		}
		navigationAssertMergedClusterGraph(t, generation, now, metas, 6, 7)
	})
}

// navigationAssertMergedClusterGraph builds the tree hubcore derives from metas,
// asserts the colliding-cluster premise, then asserts the merged project the
// projection serves validates and carries every folded member exactly once.
//
// wantClusterMembers is the number of sessions the two colliding clusters fold
// together and wantSessions is the number of distinct sessions the merged
// project must serve. Each must appear exactly once among the merged project's
// rows, and the identity must appear as one cluster row (not one per original
// group); the two counts differ when the project also holds a row outside the
// clusters.
func navigationAssertMergedClusterGraph(t *testing.T, generation string, now time.Time, metas []schema.SessionMeta, wantClusterMembers, wantSessions int) {
	t.Helper()
	tree := hubcore.BuildTreeAtWithProjects(metas, nil, nil, now, nil)

	// Reachability premise: hubcore minted one cluster id for both groups. If
	// this stops holding - because the cluster id derivation stops being a
	// function of the display name and title, or the groups stop sharing a
	// display name - there is nothing to merge and this test must be revisited.
	var clusterIDs []string
	groups := 0
	for _, project := range tree.Projects {
		if project.Key != "no-project" || project.Name != "shared" {
			t.Fatalf("tree group = key %q name %q, want the unresolved shared-basename group", project.Key, project.Name)
		}
		groups++
		for _, tier := range []string{"current", "recent", "archived"} {
			rows, _ := project.TierRows(tier)
			for _, row := range rows {
				if row.Kind == "cluster" {
					clusterIDs = append(clusterIDs, row.ID)
				}
			}
		}
	}
	if groups != 2 || len(clusterIDs) != 2 {
		t.Fatalf("tree = %d groups, %d cluster rows; want two groups each minting a cluster", groups, len(clusterIDs))
	}
	if clusterIDs[0] != clusterIDs[1] {
		t.Fatalf("cluster ids = %q and %q, want the same id so the merger must reconcile them", clusterIDs[0], clusterIDs[1])
	}

	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: generation, Revision: 1, Tree: tree})
	if err != nil {
		t.Fatal(err)
	}

	// The project the client hydrates is the graph that must stay valid: it
	// normalizes all three tiers into one document keyed by the session ref.
	project, ok := projection.Project("no-project")
	if !ok {
		t.Fatal("merged project missing from the project map")
	}
	// Every folded member is served exactly once, and the two groups' clusters
	// collapse to a single row carrying every member. This runs before the
	// schema check so a collision reports its precise cause - the same session
	// ref, and so the same entity key, appearing twice - rather than only the
	// "graph" category the validator collapses it into.
	counts := make(map[string]int)
	clusters := 0
	clusterCount := 0
	for _, tier := range []hubapi.NavigationArray[hubapi.NavigationSessionSummary]{project.Current.Sessions, project.Recent.Sessions, project.Archived.Sessions} {
		for _, session := range tier {
			if session.Kind == "cluster" {
				clusters++
				clusterCount = session.ClusterCount
			}
			navigationCountSessions(session, counts)
		}
	}
	if clusters != 1 {
		t.Errorf("merged project cluster rows = %d, want one row per cluster identity", clusters)
	}
	if clusterCount != wantClusterMembers {
		t.Errorf("merged cluster count = %d, want %d folded members", clusterCount, wantClusterMembers)
	}
	for id, count := range counts {
		if count != 1 {
			t.Errorf("session %q served %d times, want exactly once", id, count)
		}
	}
	if len(counts) != wantSessions {
		t.Errorf("merged project serves %d distinct sessions, want %d: a group's members were lost", len(counts), wantSessions)
	}

	// The project resource normalizes all three tiers into one document keyed by
	// the session ref; two rows with one ref make that document fail validation.
	if _, err := normalizeNavigationResource(navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "no-project"}, project); err != nil {
		t.Fatalf("merged project snapshot failed schema validation: %v", err)
	}

	// The same identity must not reappear through a project page either.
	page, err := projection.ProjectPage("no-project", "current", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeNavigationResource(navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "no-project", Tier: "current", Limit: 100}, page); err != nil {
		t.Fatalf("merged project page failed schema validation: %v", err)
	}

	// The catalog page (which normalizes only project summaries) must also stay
	// valid: it is the read that first exposed the duplicate-key problem.
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Projects) != 1 {
		t.Fatalf("catalog rows = %d, want one row per project key", len(catalog.Projects))
	}
	if _, err := normalizeNavigationResource(navigationResourceKey{Kind: navigationResourceProjects, Limit: 100}, catalog); err != nil {
		t.Fatalf("catalog snapshot failed schema validation: %v", err)
	}
}

// navigationCountSessions tallies every session id in a summary subtree,
// including the members folded beneath a synthetic cluster row.
func navigationCountSessions(session hubapi.NavigationSessionSummary, counts map[string]int) {
	if session.SessionID != "" && session.Kind != "cluster" {
		counts[session.SessionID]++
	}
	for _, child := range session.Children {
		navigationCountSessions(child, counts)
	}
}
