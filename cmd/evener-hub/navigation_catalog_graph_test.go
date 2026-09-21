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
		})
	}
}
