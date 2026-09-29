package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
)

// archivedRows builds n ended session rows in the rail's order.
func archivedRows(prefix string, n int, updated func(i int) time.Time, title func(i int) string) []hubcore.TreeNode {
	rows := make([]hubcore.TreeNode, n)
	for i := range rows {
		rows[i] = hubcore.TreeNode{ID: fmt.Sprintf("%s-%03d", prefix, i), Title: title(i), Kind: "session", State: "ended", UpdatedAt: updated(i), CreatedAt: updated(i)}
	}
	sort.SliceStable(rows, func(a, b int) bool { return hubcore.TreeNodeLess(rows[a], rows[b]) })
	return rows
}

func archivedProjection(t *testing.T, projects ...hubcore.TreeProject) navigationProjection {
	t.Helper()
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Revision: 1, Tree: hubcore.Tree{Projects: projects}})
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

// pageAllArchived reads every page of a project's archived list at limit and
// returns the session IDs in the order served, and the reported total.
func pageAllArchived(t *testing.T, p navigationProjection, catalog navigationResourceKind, key string, limit int) ([]string, int) {
	t.Helper()
	var ids []string
	request := navigationArchivedListRequest{Catalog: catalog, ProjectKey: key, Limit: limit}
	for pages := 0; ; pages++ {
		if pages > 10_000 {
			t.Fatal("paging did not terminate")
		}
		page, err := p.ArchivedList(request)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Sessions {
			ids = append(ids, row.SessionID)
		}
		if page.NextCursor == "" {
			return ids, page.Total
		}
		after, err := decodeArchivedCursor(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		request.After = &after
	}
}

func TestArchivedListPagesEveryRowOnceAcrossTies(t *testing.T) {
	same := time.Unix(100, 0).UTC()
	titles := []string{"Alpha", "alpha", "beta", "Beta", "gamma"}
	rows := archivedRows("session", 23, func(int) time.Time { return same }, func(i int) string { return titles[i%len(titles)] })
	rows = append(rows, hubcore.TreeNode{ID: "session-zero", Title: "zero", Kind: "session", State: "ended"})
	p := archivedProjection(t, hubcore.TreeProject{Key: "project", Name: "project", Archived: rows})
	for limit := 1; limit <= len(rows)+1; limit++ {
		ids, total := pageAllArchived(t, p, navigationResourceProjects, "project", limit)
		if total != len(rows) || len(ids) != len(rows) {
			t.Fatalf("limit %d: total=%d ids=%d, want %d", limit, total, len(ids), len(rows))
		}
		for i, row := range rows {
			if ids[i] != row.ID {
				t.Fatalf("limit %d: row %d = %s, want %s", limit, i, ids[i], row.ID)
			}
		}
	}
}

func TestArchivedListReadsTheNamedCatalogsProject(t *testing.T) {
	at := func(base int) func(int) time.Time {
		return func(i int) time.Time { return time.Unix(int64(base+i), 0).UTC() }
	}
	named := func(prefix string) func(int) string {
		return func(i int) string { return fmt.Sprintf("%s %d", prefix, i) }
	}
	active := hubcore.TreeProject{Key: "shared", Name: "shared", Archived: archivedRows("active", 2, at(10), named("active"))}
	whole := hubcore.TreeProject{Key: "shared", Name: "shared", IsArchived: true, Archived: archivedRows("whole", 5, at(20), named("whole"))}
	runs := hubcore.TreeProject{Key: "shared", Name: "shared", IsTestRun: true, Archived: archivedRows("run", 3, at(30), named("run"))}
	p := archivedProjection(t, active, whole, runs)
	for _, tc := range []struct {
		catalog navigationResourceKind
		want    int
	}{{navigationResourceProjects, 2}, {navigationResourceArchivedProjects, 5}, {navigationResourceTestRuns, 3}} {
		if _, total := pageAllArchived(t, p, tc.catalog, "shared", 50); total != tc.want {
			t.Fatalf("%s total=%d, want %d", tc.catalog, total, tc.want)
		}
	}
}

func TestArchivedListUnknownProjectIsAnEmptyPage(t *testing.T) {
	p := archivedProjection(t)
	page, err := p.ArchivedList(navigationArchivedListRequest{Catalog: navigationResourceProjects, ProjectKey: "missing"})
	if err != nil || len(page.Sessions) != 0 || page.Sessions == nil || page.Total != 0 || page.NextCursor != "" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

// The cursor finds its place by binary search, which is only right if every
// archived tier is already in the rail's order: the tree's own tier and a tier
// the projection merged from two groups with one key.
func TestArchivedTiersAreInSessionOrder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	old := now.Add(-30 * 24 * time.Hour)
	titles := []string{"Alpha", "alpha", "beta", " Beta", "gamma"}
	metas := make([]schema.SessionMeta, 0, 70)
	for i := range 70 {
		updated := old.Add(time.Duration(i%7) * time.Minute)
		if i%11 == 0 {
			updated = time.Time{}
		}
		metas = append(metas, schema.SessionMeta{ID: fmt.Sprintf("session-order-%03d", i), Name: fmt.Sprintf("%s %02d", titles[i%len(titles)], i/len(titles)), CreatedAt: old.Add(time.Duration(i%3) * time.Minute), UpdatedAt: updated, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/order"}})
	}
	tree := hubcore.BuildTreeAt(metas, nil, map[hubcore.ArchiveKey]bool{}, now)
	built := append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...)
	if len(built) != 1 {
		t.Fatalf("tree built %d projects, want 1", len(built))
	}
	merged := hubcore.TreeProject{Key: built[0].Key, Name: "second group", IsArchived: built[0].IsArchived, Archived: archivedRows("merged", 9, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return titles[i%len(titles)] })}
	p := archivedProjection(t, built[0], merged)
	if got := len(p.catalogs[navigationResourceProjects]) + len(p.catalogs[navigationResourceArchivedProjects]); got != 1 {
		t.Fatalf("projection has %d projects, want the two groups merged into 1", got)
	}
	for _, project := range append(p.catalogs[navigationResourceProjects], p.catalogs[navigationResourceArchivedProjects]...) {
		rows, _ := project.TierRows("archived")
		if len(rows) != 79 {
			t.Fatalf("project %s has %d archived rows, want 79 (70 built + 9 merged)", project.Key, len(rows))
		}
		if !sort.SliceIsSorted(rows, func(a, b int) bool { return hubcore.TreeNodeLess(rows[a], rows[b]) }) {
			t.Fatalf("project %s archived tier is not in session order", project.Key)
		}
	}
}

func dispatchArchivedList(t *testing.T, server *appserver.Server, params appwire.ArchivedListParams) (appwire.ArchivedListResponse, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, err := server.Router().Dispatch(context.Background(), appwire.Request{ID: appwire.NewIntID(1), Method: appwire.MethodEvenerArchivedList, Params: raw})
	if err != nil {
		return appwire.ArchivedListResponse{}, err
	}
	response, ok := result.(appwire.ArchivedListResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.ArchivedListResponse", result)
	}
	return response, nil
}

func TestHubArchivedListServesTheArchivedTierAndRejectsBadRequests(t *testing.T) {
	source := newTestNavigationSource(testNavigationNow())
	old := testNavigationNow().Add(-30 * 24 * time.Hour)
	source.inputs.Tree.Projects[0].Archived = archivedRows("archived", 3, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return fmt.Sprintf("archived %d", i) })
	service := newTestNavigationService(t, source)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerArchivedListHandler(server, service)
	key := source.inputs.Tree.Projects[0].Key

	first, err := dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "projects", ProjectKey: key, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	var rows []hubapi.NavigationSessionSummary
	if err := json.Unmarshal(first.Sessions, &rows); err != nil {
		t.Fatal(err)
	}
	if first.Total != 3 || len(rows) != 2 || rows[0].SessionID != "archived-002" || first.NextCursor == "" {
		t.Fatalf("first page = total %d rows %+v cursor %q", first.Total, rows, first.NextCursor)
	}
	last, err := dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "projects", ProjectKey: key, Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(last.Sessions, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionID != "archived-000" || last.NextCursor != "" {
		t.Fatalf("last page = rows %+v cursor %q", rows, last.NextCursor)
	}

	_, err = dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "bogus", ProjectKey: key})
	assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	_, err = dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "projects", ProjectKey: key, Cursor: "!"})
	assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	for _, params := range []appwire.ArchivedListParams{
		{Catalog: "projects", ProjectKey: ""},
		{Catalog: "projects", ProjectKey: key, Limit: -1},
		{Catalog: "projects", ProjectKey: key, Limit: maxNavigationSectionRows + 1},
	} {
		_, err = dispatchArchivedList(t, server, params)
		assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	}
}
