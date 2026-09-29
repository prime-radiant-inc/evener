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
		after, err := decodeArchivedCursor(page.NextCursor, catalog, key)
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

// An absent limit pages at the maximum, not the whole tier.
func TestHubArchivedListOmittedLimitServesOneMaximumPage(t *testing.T) {
	source := newTestNavigationSource(testNavigationNow())
	old := testNavigationNow().Add(-30 * 24 * time.Hour)
	source.inputs.Tree.Projects[0].Archived = archivedRows("archived", maxNavigationSectionRows+10, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return fmt.Sprintf("archived %d", i) })
	service := newTestNavigationService(t, source)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerArchivedListHandler(server, service)
	response, err := dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "projects", ProjectKey: source.inputs.Tree.Projects[0].Key})
	if err != nil {
		t.Fatal(err)
	}
	var rows []hubapi.NavigationSessionSummary
	if err := json.Unmarshal(response.Sessions, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != maxNavigationSectionRows || response.NextCursor == "" || response.Total != maxNavigationSectionRows+10 {
		t.Fatalf("omitted limit served %d rows, cursor %q, total %d", len(rows), response.NextCursor, response.Total)
	}
}

// A cursor names the list it continues: one minted for another project, or
// for the same key in another catalog, is rejected rather than misapplied.
func TestHubArchivedListRejectsACursorFromAnotherList(t *testing.T) {
	source := newTestNavigationSource(testNavigationNow())
	old := testNavigationNow().Add(-30 * 24 * time.Hour)
	source.inputs.Tree.Projects[0].Archived = archivedRows("archived", 3, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return fmt.Sprintf("archived %d", i) })
	service := newTestNavigationService(t, source)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerArchivedListHandler(server, service)
	key := source.inputs.Tree.Projects[0].Key
	first, err := dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "projects", ProjectKey: key, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page: cursor %q, err %v", first.NextCursor, err)
	}
	for _, params := range []appwire.ArchivedListParams{
		{Catalog: "projects", ProjectKey: "another-project", Cursor: first.NextCursor},
		{Catalog: "archived_projects", ProjectKey: key, Cursor: first.NextCursor},
	} {
		_, err := dispatchArchivedList(t, server, params)
		assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	}
}

// An empty page carries an empty array, never null.
func TestHubArchivedListEmptyPageIsAnEmptyArray(t *testing.T) {
	service := newTestNavigationService(t, newTestNavigationSource(testNavigationNow()))
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerArchivedListHandler(server, service)
	response, err := dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "projects", ProjectKey: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Sessions) != "[]" {
		t.Fatalf("sessions = %s, want []", response.Sessions)
	}
}

// more_archived on a project summary is the project's archived session count:
// the count the rail's Archived fold shows, and the total the archived list
// pages through. It is the uncapped tier, not the beyond-cap remainder.
func TestProjectSummaryMoreArchivedIsTheArchivedCount(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	old := now.Add(-30 * 24 * time.Hour)
	metas := make([]schema.SessionMeta, 0, 62)
	for i := range 60 {
		metas = append(metas, schema.SessionMeta{ID: fmt.Sprintf("session-capped-%03d", i), Name: fmt.Sprintf("capped %d", i), CreatedAt: old, UpdatedAt: old.Add(time.Duration(i) * time.Second), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/capped"}})
	}
	for i := range 2 {
		metas = append(metas, schema.SessionMeta{ID: fmt.Sprintf("session-current-%d", i), Name: fmt.Sprintf("current %d", i), CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/capped"}})
	}
	tree := hubcore.BuildTreeAt(metas, nil, map[hubcore.ArchiveKey]bool{}, now)
	small := hubcore.TreeProject{Key: "small", Name: "small", Archived: archivedRows("small", 3, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return fmt.Sprintf("small %d", i) })}
	whole := hubcore.TreeProject{Key: "small", Name: "small", IsArchived: true, Archived: archivedRows("whole", 4, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return fmt.Sprintf("whole %d", i) })}
	p := archivedProjection(t, append(append([]hubcore.TreeProject(nil), tree.Projects...), small, whole)...)
	for _, catalog := range []navigationResourceKind{navigationResourceProjects, navigationResourceArchivedProjects} {
		summaries, err := p.CatalogPage(catalog, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, summary := range summaries.Projects {
			ids, total := pageAllArchived(t, p, catalog, summary.Key, 50)
			if summary.MoreArchived != total || total != len(ids) {
				t.Fatalf("%s %s: more_archived=%d, list total=%d, listed=%d", catalog, summary.Key, summary.MoreArchived, total, len(ids))
			}
		}
	}
	capped, err := p.CatalogPage(navigationResourceProjects, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range capped.Projects {
		if summary.Key != "small" && summary.MoreArchived != 60 {
			t.Fatalf("capped project more_archived=%d, want 60", summary.MoreArchived)
		}
	}
}

// Navigation carries a project's archived count, never its archived rows: the
// project resource's archived tier is empty with nothing remaining, its
// archived page is empty, and an archived row's change moves no project
// fingerprint. Locations and pin sections still index archived rows.
func TestNavigationServesNoArchivedRows(t *testing.T) {
	old := time.Unix(1_600_000_000, 0).UTC()
	current := []hubcore.TreeNode{{ID: "session-now", Title: "now", Kind: "session", State: "idle", UpdatedAt: old.Add(time.Hour)}}
	archived := archivedRows("archived", 3, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return fmt.Sprintf("archived %d", i) })
	build := func(archived []hubcore.TreeNode) navigationProjection {
		return archivedProjection(t,
			hubcore.TreeProject{Key: "project", Name: "project", Current: current, Archived: archived},
			hubcore.TreeProject{Key: "only-archived", Name: "only archived", Archived: archivedRows("only", 2, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return fmt.Sprintf("only %d", i) })},
		)
	}
	p := build(archived)

	resource, ok := p.Project("project")
	if !ok || len(resource.Current.Sessions) != 1 || len(resource.Archived.Sessions) != 0 || resource.Archived.Remaining != 0 {
		t.Fatalf("project resource = current %d, archived %d remaining %d", len(resource.Current.Sessions), len(resource.Archived.Sessions), resource.Archived.Remaining)
	}
	onlyArchived, _ := p.Project("only-archived")
	if err := validateNavigationPageProgress(navigationResourceProject, onlyArchived); err != nil {
		t.Fatalf("a project whose only rows are archived fails page progress: %v", err)
	}
	page, err := p.ProjectPage("project", "archived", 0, 50)
	if err != nil || len(page.Sessions) != 0 || page.Remaining != 0 {
		t.Fatalf("archived page = %d rows, remaining %d, err %v", len(page.Sessions), page.Remaining, err)
	}
	location, ok := p.Location("local:archived-001")
	if !ok || location.Tier != "archived" || location.ProjectKey != "project" || !location.TopLevel {
		t.Fatalf("archived location = %#v, found %v", location, ok)
	}

	renamed := append([]hubcore.TreeNode(nil), archived...)
	renamed[0].Title = "renamed archived row"
	before, _, err := navigationLogicalFingerprintsContext(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := navigationLogicalFingerprintsContext(context.Background(), build(renamed))
	if err != nil {
		t.Fatal(err)
	}
	key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "project"}
	if before[key] != after[key] {
		t.Fatal("renaming an archived row moved the project fingerprint")
	}
	// The archived count still rides the project summary, so archiving one
	// more session moves the fingerprint and the rail refetches its list.
	grown := append(append([]hubcore.TreeNode(nil), archived...), hubcore.TreeNode{ID: "session-archived-new", Title: "newly archived", Kind: "session", State: "idle", UpdatedAt: old.Add(-time.Hour)})
	moved, _, err := navigationLogicalFingerprintsContext(context.Background(), build(grown))
	if err != nil {
		t.Fatal(err)
	}
	if before[key] == moved[key] {
		t.Fatal("archiving one more session left the project fingerprint unchanged")
	}
}

// The out-of-range limit error states the range the parser accepts: 0 is
// accepted as absent, so the message must not claim the minimum is 1.
func TestParseNavigationArchivedListParamsLimitErrorNamesAcceptedRange(t *testing.T) {
	const want = "limit must be between 0 and 50 (0 or absent means 50)"
	for _, limit := range []int{-1, maxNavigationSectionRows + 1} {
		_, err := parseNavigationArchivedListParams(appwire.ArchivedListParams{Catalog: "projects", ProjectKey: "project", Limit: limit})
		if err == nil || err.Error() != want {
			t.Fatalf("limit %d: error = %v, want %q", limit, err, want)
		}
	}
	if _, err := parseNavigationArchivedListParams(appwire.ArchivedListParams{Catalog: "projects", ProjectKey: "project", Limit: 0}); err != nil {
		t.Fatalf("limit 0 must be accepted as absent: %v", err)
	}
}
