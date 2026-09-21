package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// navigationCatalogSourceWithThreads is a scripted remote source that serves
// more than one thread. A real attached host does exactly this, and the two
// threads are what make the duplicate-key shape reachable: each thread whose
// working directory resolves to no controller-local project and carries no
// carried project identity becomes its own tree group, and every one of those
// groups presents the shared "no-project" wire key.
type navigationCatalogSourceWithThreads struct {
	*scriptedAppSource
	threads []appwire.Thread
}

func (s *navigationCatalogSourceWithThreads) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: s.threads}, nil
}

// A host that is merely attached must not be able to fail the catalog reads.
// The tree groups sessions by canonical project identity, but the project's
// wire Key is an *address*, not a unique row id: sessions whose working
// directory resolves to no project (and that carry no carried identity) all
// group under the shared "no-project" key while keeping their own grouping
// path. Two such groups - or two of any tree projects that present one Key -
// become two catalog rows, and two rows with one Key collapse onto a single
// entity key. The navigation graph then fails its schema check with the
// "graph" category, which is the live failure in issue #2123: the manifest
// read succeeded while archived_projects answered an internal error.
func TestNavigationCatalogGraphStaysValidWithASecondSource(t *testing.T) {
	// Both working directories are host-side paths the controller cannot
	// resolve, and neither thread carries a carried project identity, so each
	// thread mints its own tree group that presents Key "no-project".
	threads := []appwire.Thread{
		{ID: "remote-one", SessionID: "remote-one", Source: "host-a", CWD: "/host-a/only/one",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
		{ID: "remote-two", SessionID: "remote-two", Source: "host-a", CWD: "/host-a/only/two",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: hubcore.NewPastIndex("")})
	registry := appsource.NewRegistry()
	registry.Add(&navigationCatalogSourceWithThreads{scriptedAppSource: &scriptedAppSource{id: "host-a"}, threads: threads})
	web.sources = registry

	service := newNavigationService(navigationServiceConfig{
		Source:     webNavigationSource{web: web},
		Generation: func() (string, error) { return "00112233445566778899aabbccddeeff", nil },
		Now:        func() time.Time { return base },
	})
	for _, kind := range []navigationResourceKind{
		navigationResourceManifest,
		navigationResourceProjects,
		navigationResourceArchivedProjects,
		navigationResourceTestRuns,
	} {
		result, err := service.readV2(t.Context(), navigationResourceKey{Kind: kind, Limit: 100}, nil)
		if err != nil {
			t.Fatalf("%s read with an attached host: %v", kind, err)
		}
		if result.Response.Status != "ok" || len(result.Response.Data) == 0 {
			t.Fatalf("%s response = %+v", kind, result.Response)
		}
	}
}

// The catalog's graph shape is exactly what the schema wants: one resource-root
// container whose children are the catalog's entity keys, one entity per
// project key. The duplicate groups collapse to a single addressable row, so
// the client can never hydrate one row's Key from another group's detail.
func TestNavigationCatalogGraphCollapsesDuplicateProjectKeys(t *testing.T) {
	generation := "00112233445566778899aabbccddeeff"
	duplicate := func(isArchived, isTestRun bool) []hubcore.TreeProject {
		return []hubcore.TreeProject{
			{Key: "no-project", Name: "one", IsArchived: isArchived, IsTestRun: isTestRun,
				Current: []hubcore.TreeNode{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Title: "one", Project: "one", Kind: "session", State: "idle"}}},
			{Key: "no-project", Name: "two", IsArchived: isArchived, IsTestRun: isTestRun,
				Current: []hubcore.TreeNode{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Title: "two", Project: "two", Kind: "session", State: "idle"}}},
		}
	}
	catalogs := map[navigationResourceKind][]hubcore.TreeProject{
		navigationResourceProjects:         duplicate(false, false),
		navigationResourceArchivedProjects: duplicate(true, false),
		navigationResourceTestRuns:         duplicate(false, true),
	}
	for kind, projects := range catalogs {
		t.Run(string(kind), func(t *testing.T) {
			tree := hubcore.Tree{}
			switch kind {
			case navigationResourceProjects:
				tree.Projects = projects
			case navigationResourceArchivedProjects:
				tree.ArchivedProjects = projects
			case navigationResourceTestRuns:
				tree.Projects = projects
			}
			projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: generation, Revision: 1, Tree: tree})
			if err != nil {
				t.Fatal(err)
			}
			resource, err := projection.CatalogPage(kind, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(resource.Projects); got != 1 {
				t.Fatalf("catalog rows = %d, want one row per project key", got)
			}
			snapshot, err := normalizeNavigationResource(navigationResourceKey{Kind: kind, Limit: 100}, resource)
			if err != nil {
				t.Fatalf("catalog snapshot failed schema validation: %v", err)
			}
			if len(snapshot.Entities) != 1 || len(snapshot.Containers) != 1 {
				t.Fatalf("shape = %d entities, %d containers; want 1 and 1", len(snapshot.Entities), len(snapshot.Containers))
			}
			root := snapshot.Containers[0]
			if root.Owner.Kind != "resource_root" || root.Owner.Slot != "projects" || len(root.Children) != 1 || root.Children[0] != snapshot.Entities[0].Key {
				t.Fatalf("root = %+v, entities = %+v", root, snapshot.Entities)
			}
			var summary hubapi.NavigationProjectSummary
			if err := json.Unmarshal(snapshot.Entities[0].Value, &summary); err != nil {
				t.Fatal(err)
			}
			if summary.Key != "no-project" {
				t.Fatalf("entity key = %q, want the collapsed no-project address", summary.Key)
			}
			// The collapse must MERGE, not discard. The retained row addresses one
			// project, and that project owns every session from every tree group
			// that shared its Key: the buckets are the sole input to the project
			// map, the manifest counts, and the location index, so a dropped group
			// loses its sessions from the project entry and from a location lookup
			// by ref.
			project, ok := projection.Project("no-project")
			if !ok {
				t.Fatal("collapsed catalog row has no project entry")
			}
			ids := navigationProjectSessionIDs(t, project)
			for _, want := range []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"} {
				if !ids[want] {
					t.Errorf("merged project sessions = %v, missing %q: the collapse dropped a group's sessions", ids, want)
				}
			}
			if summary.SessionCount != 2 {
				t.Errorf("catalog row session count = %d, want 2 (both groups)", summary.SessionCount)
			}
			// A merged group's session must still resolve through the location
			// index. Read it the way the live path does - projection.Resource on a
			// location key - so a dropped session surfaces as the "not found" shape
			// rather than as a silently empty location.
			for _, id := range []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"} {
				ref := hubapi.LocalRef(id).String()
				object, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLocation, ID: ref})
				if err != nil {
					t.Errorf("location read for merged session %q: %v", id, err)
					continue
				}
				location, ok := object.(hubapi.NavigationSessionLocation)
				if !ok {
					t.Errorf("location read for %q returned %T", id, object)
					continue
				}
				if location.ProjectKey != "no-project" || location.Session == nil || location.Session.SessionID != id {
					t.Fatalf("location for %q = %+v", id, location)
				}
			}
		})
	}
}

// A Key present in two different buckets keeps a row in each catalog. Each
// catalog page is validated on its own, so a cross-bucket repeat cannot produce
// the duplicate-entity-key error; collapsing across buckets would instead empty
// an unrelated catalog. This test pins that each bucket keeps its own row and
// that both rows' sessions stay indexed.
func TestNavigationCatalogGraphKeepsDuplicateKeyPerBucket(t *testing.T) {
	generation := "00112233445566778899aabbccddeeff"
	active := hubcore.TreeProject{Key: "no-project", Name: "active", Current: []hubcore.TreeNode{
		{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Title: "one", Project: "one", Kind: "session", State: "idle"},
	}}
	archived := hubcore.TreeProject{Key: "no-project", Name: "archived", IsArchived: true, Current: []hubcore.TreeNode{
		{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Title: "two", Project: "two", Kind: "session", State: "idle"},
	}}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: generation, Revision: 1,
		Tree: hubcore.Tree{Projects: []hubcore.TreeProject{active}, ArchivedProjects: []hubcore.TreeProject{archived}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []navigationResourceKind{navigationResourceProjects, navigationResourceArchivedProjects} {
		resource, err := projection.CatalogPage(kind, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(resource.Projects) != 1 {
			t.Fatalf("%s rows = %d, want one row for the bucket's own group", kind, len(resource.Projects))
		}
		snapshot, err := normalizeNavigationResource(navigationResourceKey{Kind: kind, Limit: 100}, resource)
		if err != nil {
			t.Fatalf("%s snapshot failed schema validation: %v", kind, err)
		}
		if len(snapshot.Entities) != 1 || resource.Projects[0].Key != "no-project" {
			t.Fatalf("%s shape = %d entities, row key %q", kind, len(snapshot.Entities), resource.Projects[0].Key)
		}
	}
	// Both buckets' rows point at the shared Key, so the project map is
	// last-write-wins; the location index must still carry every session of
	// every catalog.
	for _, id := range []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"} {
		ref := hubapi.LocalRef(id).String()
		object, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLocation, ID: ref})
		if err != nil {
			t.Errorf("location read for merged session %q: %v", id, err)
			continue
		}
		location, ok := object.(hubapi.NavigationSessionLocation)
		if !ok || location.Session == nil || location.Session.SessionID != id {
			t.Fatalf("location for %q = %T %+v", id, object, location)
		}
	}
}

// navigationProjectSessionIDs collects the session ids a project resource
// exposes across its tiers, so a merge assertion can name the sessions that
// survived instead of merely counting them.
func navigationProjectSessionIDs(t *testing.T, resource hubapi.NavigationProjectResource) map[string]bool {
	t.Helper()
	ids := make(map[string]bool)
	tiers := []hubapi.NavigationArray[hubapi.NavigationSessionSummary]{
		resource.Current.Sessions,
		resource.Recent.Sessions,
		resource.Archived.Sessions,
	}
	for _, tier := range tiers {
		for _, session := range tier {
			ids[session.SessionID] = true
		}
	}
	return ids
}

// navigationMergeGraphID renders a two-digit row suffix without pulling fmt into
// this test file (which imports no formatter).
func navigationMergeGraphID(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// The catalog merge must not only keep every group's sessions; it must describe
// them the way a tree-built project does. hubcore caps a tree-built project's
// public tier at maxSidebarSessionsPerTier and reports the rows beyond it as the
// per-tier overflow (More*), and its tiers are globally most-recent-first. A
// merged row colliding on one Key must match: two groups that each exceed the
// cap collapse to a project whose current overflow is the number of rows beyond
// the cap, whose retained tier keeps every row so paging still reaches them, and
// whose rows stay ordered by recency across both groups rather than in
// group-append order.
func TestNavigationCatalogGraphMergeCapsTiersAndKeepsRecency(t *testing.T) {
	generation := "00112233445566778899aabbccddeeff"
	const capacity = hubcore.SidebarSessionPageSize
	base := time.Unix(1_700_000_000, 0).UTC()
	// capacity+5 current rows per group: each group overflows the cap on its
	// own, so neither group's own MoreCurrent is set, yet the merged project
	// overflows it. The "a" rows are two hours older than the "b" rows, so the
	// newest session overall lives in the SECOND group and only a global
	// re-sort lifts it above the whole first group.
	group := func(prefix string, offset time.Duration) []hubcore.TreeNode {
		rows := make([]hubcore.TreeNode, capacity+5)
		for index := range rows {
			updated := base.Add(-offset - time.Duration(index)*time.Minute)
			id := prefix + "-" + navigationMergeGraphID(index)
			rows[index] = hubcore.TreeNode{
				ID:        id,
				Title:     id,
				Project:   prefix,
				Kind:      "session",
				State:     "idle",
				CreatedAt: updated,
				UpdatedAt: updated,
			}
		}
		return rows
	}
	first := hubcore.TreeProject{Key: "no-project", Name: "one", Current: group("a", 2*time.Hour)}
	second := hubcore.TreeProject{Key: "no-project", Name: "two", Current: group("b", 0)}
	total := len(first.Current) + len(second.Current)
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: generation, Revision: 1,
		Tree: hubcore.Tree{Projects: []hubcore.TreeProject{first, second}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The merged row states the overflow a tree-built project with the same
	// sessions would: rows beyond the cap, not the sum of the groups' own
	// (zero) overflow counts. The retained tier keeps every row, because
	// TierRows falls back to it once the merge cannot carry hubcore's private
	// slices forward.
	merged := projection.projects["no-project"]
	if len(merged.Current) != total {
		t.Errorf("merged current tier = %d rows, want all %d retained for TierRows", len(merged.Current), total)
	}
	if merged.MoreCurrent != total-capacity {
		t.Errorf("merged MoreCurrent = %d, want %d (total %d minus the kept cap)", merged.MoreCurrent, total-capacity, total)
	}
	if merged.MoreRecent != 0 || merged.MoreArchived != 0 {
		t.Errorf("merged MoreRecent/MoreArchived = %d/%d, want 0/0", merged.MoreRecent, merged.MoreArchived)
	}

	// The catalog row a client reads carries the same shape: every session
	// counted and the overflow beyond the cap stated.
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Projects) != 1 {
		t.Fatalf("catalog rows = %d, want one row per project key", len(catalog.Projects))
	}
	if summary := catalog.Projects[0]; summary.SessionCount != total || summary.MoreCurrent != total-capacity {
		t.Errorf("catalog summary = %d sessions, %d more current; want %d and %d", summary.SessionCount, summary.MoreCurrent, total, total-capacity)
	}

	// The retained rows are globally most-recent-first: the newest session of
	// the second group sorts ahead of every row of the older first group.
	// Appending group one before group two would leave "a-00" first.
	if merged.Current[0].ID != "b-00" {
		t.Errorf("merged current[0] = %q, want the newest row across both groups (b-00); tiers kept group-append order", merged.Current[0].ID)
	}

	// The project detail serves the same order, caps the initial page at the
	// public tier, and pages through the retained rows beyond the cap instead of
	// losing them to the merge.
	project, ok := projection.Project("no-project")
	if !ok {
		t.Fatal("merged project missing from the project map")
	}
	if len(project.Current.Sessions) != capacity || project.Current.Sessions[0].SessionID != "b-00" {
		t.Errorf("project current page = %d rows starting %q, want %d starting b-00", len(project.Current.Sessions), project.Current.Sessions[0].SessionID, capacity)
	}
	if project.Current.Remaining != total-capacity {
		t.Errorf("project current remaining = %d, want %d", project.Current.Remaining, total-capacity)
	}
	page, err := projection.ProjectPage("no-project", "current", capacity, capacity)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != capacity || page.Sessions[0].SessionID != "b-50" || page.Remaining != total-2*capacity {
		t.Errorf("project page at offset %d = %d rows starting %q, %d remaining; want %d starting b-50, %d remaining", capacity, len(page.Sessions), page.Sessions[0].SessionID, page.Remaining, capacity, total-2*capacity)
	}

	// A session that only the retained union reaches keeps its location, so a
	// direct lookup still resolves it after the merge.
	last := "a-54"
	ref := hubapi.LocalRef(last).String()
	object, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLocation, ID: ref})
	if err != nil {
		t.Fatalf("location for beyond-cap session %q: %v", last, err)
	}
	location, ok := object.(hubapi.NavigationSessionLocation)
	if !ok || location.Session == nil || location.Session.SessionID != last {
		t.Fatalf("location for %q = %T %+v", last, object, location)
	}
}
