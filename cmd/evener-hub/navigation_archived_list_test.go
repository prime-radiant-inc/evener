package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
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

// pageAllArchived reads every page of a project's archived list at limit the
// way a client does, sending each page's cursor back with the same catalog
// hint, and returns the session IDs in the order served, and the reported
// total.
func pageAllArchived(t *testing.T, p navigationProjection, catalog navigationResourceKind, key string, limit int) ([]string, int) {
	t.Helper()
	var ids []string
	params := appwire.ArchivedListParams{Catalog: string(catalog), ProjectKey: key, Limit: limit}
	for pages := 0; ; pages++ {
		if pages > 10_000 {
			t.Fatal("paging did not terminate")
		}
		request, err := parseNavigationArchivedListParams(params)
		if err != nil {
			t.Fatalf("hint %q, page %d: %v", catalog, pages, err)
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
		params.Cursor = page.NextCursor
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

// A key no catalog holds reads no catalog and answers an empty page, with a
// hint or without one.
func TestArchivedListUnknownProjectIsAnEmptyPage(t *testing.T) {
	p := archivedProjection(t)
	for _, hint := range []navigationResourceKind{"", navigationResourceProjects, navigationResourceArchivedProjects} {
		page, err := p.ArchivedList(navigationArchivedListRequest{Hint: hint, ProjectKey: "missing"})
		if err != nil || len(page.Sessions) != 0 || page.Sessions == nil || page.Total != 0 || page.NextCursor != "" || page.Catalog != "" {
			t.Fatalf("hint %q: page=%#v err=%v", hint, page, err)
		}
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

// archivedListServer serves evener/archived/list over a test navigation source
// whose first project has n archived rows, and returns that project's key.
func archivedListServer(t *testing.T, n int) (*appserver.Server, string) {
	t.Helper()
	source := newTestNavigationSource(testNavigationNow())
	old := testNavigationNow().Add(-30 * 24 * time.Hour)
	source.inputs.Tree.Projects[0].Archived = archivedRows("archived", n, func(i int) time.Time { return old.Add(time.Duration(i) * time.Minute) }, func(i int) string { return fmt.Sprintf("archived %d", i) })
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerArchivedListHandler(server, newTestNavigationService(t, source))
	return server, source.inputs.Tree.Projects[0].Key
}

func TestHubArchivedListServesTheArchivedTierAndRejectsBadRequests(t *testing.T) {
	server, key := archivedListServer(t, 3)

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
	server, key := archivedListServer(t, maxNavigationSectionRows+10)
	response, err := dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "projects", ProjectKey: key})
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
	server, key := archivedListServer(t, 3)
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

func TestHubArchivedListPreservesForkOriginalsFromAuthoritativeTree(t *testing.T) {
	now := testNavigationNow()
	old := now.Add(-40 * 24 * time.Hour)
	env := schema.EnvironmentInfo{WorkingDir: "/projects/fork"}
	tree := hubcore.BuildTreeAt([]schema.SessionMeta{
		{ID: "orig", ForkLabel: "before edit", CreatedAt: old, UpdatedAt: old, EnvInfo: env},
		{ID: "cont", ParentSessionID: "orig", CreatedAt: old, UpdatedAt: old, EnvInfo: env},
	}, nil, nil, now)
	if len(tree.ArchivedProjects) != 1 || len(tree.ArchivedProjects[0].Archived) != 1 {
		t.Fatalf("authoritative archived tree: %+v", tree)
	}
	project := &tree.ArchivedProjects[0]
	row := &project.Archived[0]
	if row.ID != "cont" || len(row.Children) != 1 || row.Children[0].ID != "orig" || row.Children[0].Kind != "fork" {
		t.Fatalf("authoritative fork ownership: %+v", row)
	}
	row.Children = append(row.Children, hubcore.TreeNode{ID: "delegate", Kind: "subagent", State: "ended"})
	source := newTestNavigationSource(now)
	source.inputs.Tree = tree
	service := newTestNavigationService(t, source)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerArchivedListHandler(server, service)
	response, err := dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: "archived_projects", ProjectKey: project.Key})
	if err != nil {
		t.Fatal(err)
	}
	var rows []hubapi.NavigationSessionSummary
	if err := json.Unmarshal(response.Sessions, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Ref != "local:cont" || len(rows[0].Children) != 1 || rows[0].Children[0].Ref != "local:orig" || rows[0].Children[0].Kind != "fork" {
		t.Fatalf("archived API loses fork-only discovery: %+v", rows)
	}
	if response.Total != 1 {
		t.Fatalf("total counts top-level sessions: %d", response.Total)
	}
}

func TestArchivedForkOriginalsRespectTraversalAndEnvelopeBounds(t *testing.T) {
	children := make([]hubcore.TreeNode, maxNavigationNodes+1)
	for i := range children {
		children[i] = hubcore.TreeNode{ID: fmt.Sprintf("original-%04d", i), Kind: "fork", State: "ended", Title: strings.Repeat("界", maxNavigationTitleRunes), LastMessage: strings.Repeat("界", appwire.MaxMessageExcerptRunes)}
	}
	first := hubcore.TreeNode{ID: "continuation", Kind: "session", State: "ended", UpdatedAt: time.Unix(200, 0), Children: children}
	second := hubcore.TreeNode{ID: "older", Kind: "session", State: "ended", UpdatedAt: time.Unix(100, 0)}
	p := archivedProjection(t, hubcore.TreeProject{Key: "project", Archived: []hubcore.TreeNode{first, second}})
	page, err := p.ArchivedList(navigationArchivedListRequest{Hint: navigationResourceProjects, ProjectKey: "project", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) == 0 {
		t.Fatal("bounded fork page contains no session")
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Ref != "local:continuation" || len(page.Sessions[0].Children) == 0 || page.Total != 2 || page.NextCursor == "" {
		t.Fatalf("bounded fork page lost progress: rows=%d children=%d total=%d cursor=%q", len(page.Sessions), len(page.Sessions[0].Children), page.Total, page.NextCursor)
	}
	if len(page.Sessions[0].Children) >= maxNavigationNodes-1 {
		t.Fatal("oversized fork page did not apply the byte budget")
	}
	assertArchivedForkAccounting(t, page.Sessions, 1+len(children))
	if nodes := navigationSummaryNodes(page.Sessions); nodes > maxNavigationNodes {
		t.Fatalf("nodes=%d", nodes)
	}
	if size := navigationEncodedSize(page); size > maxNavigationResponseBytes {
		t.Fatalf("bytes=%d", size)
	}
	after, err := decodeArchivedCursor(page.NextCursor, navigationResourceProjects, "project")
	if err != nil {
		t.Fatal(err)
	}
	next, err := p.ArchivedList(navigationArchivedListRequest{Hint: navigationResourceProjects, ProjectKey: "project", Limit: 50, After: &after})
	if err != nil || len(next.Sessions) != 1 || next.Sessions[0].Ref != "local:older" || next.NextCursor != "" {
		t.Fatalf("continuation=%+v err=%v", next, err)
	}

	deep := hubcore.TreeNode{ID: "deepest", Kind: "fork", State: "ended"}
	for i := range maxNavigationDepth + 5 {
		deep = hubcore.TreeNode{ID: fmt.Sprintf("fork-%d", i), Kind: "fork", State: "ended", Children: []hubcore.TreeNode{deep}}
	}
	bounded := navigationProjector{projection: p}
	summary, ok := bounded.projectArchivedNode(deep, 1)
	if !ok || !bounded.truncated || navigationSummaryNodes(hubapi.NavigationArray[hubapi.NavigationSessionSummary]{summary}) != maxNavigationDepth {
		t.Fatalf("depth bound: ok=%v truncated=%v depth=%d", ok, bounded.truncated, bounded.depth)
	}
}

func assertArchivedForkAccounting(t *testing.T, rows []hubapi.NavigationSessionSummary, want int) {
	t.Helper()
	var visit func([]hubapi.NavigationSessionSummary) int
	visit = func(rows []hubapi.NavigationSessionSummary) int {
		weight := 0
		for _, row := range rows {
			if row.Kind == "subagent" || row.MoreSubagents != 0 {
				t.Fatalf("archived fork accounting includes activity: %+v", row)
			}
			weight += 1 + row.OmittedDescendants + visit(row.Children)
		}
		return weight
	}
	if got := visit(rows); got != want {
		t.Fatalf("archived fork accounting: published=%d accounted=%d want=%d", navigationSummaryNodes(rows), got, want)
	}
}

func TestArchivedForkDepthCapAccountsForOmittedOriginals(t *testing.T) {
	now := testNavigationNow()
	old := now.Add(-40 * 24 * time.Hour)
	env := schema.EnvironmentInfo{WorkingDir: "/projects/long-fork"}
	var metas []schema.SessionMeta
	for i := range maxNavigationDepth + 5 {
		meta := schema.SessionMeta{ID: fmt.Sprintf("fork-%02d", i), CreatedAt: old, UpdatedAt: old, EnvInfo: env}
		if i > 0 {
			meta.ParentSessionID = fmt.Sprintf("fork-%02d", i-1)
		}
		if i < maxNavigationDepth+4 {
			meta.ForkLabel = "before edit"
		}
		metas = append(metas, meta)
	}
	tree := hubcore.BuildTreeAt(metas, nil, nil, now)
	if len(tree.ArchivedProjects) != 1 || len(tree.ArchivedProjects[0].Archived) != 1 {
		t.Fatalf("authoritative archived tree: %+v", tree)
	}
	project := &tree.ArchivedProjects[0]
	if nodes := countTreeNodes(project.Archived); nodes != len(metas) {
		t.Fatalf("authoritative fork nodes=%d want=%d", nodes, len(metas))
	}
	// A delegate branch beyond the cutoff is outside this archived discovery,
	// including a fork original that belongs to that delegate conversation.
	tail := &project.Archived[0]
	for range maxNavigationDepth {
		tail = &tail.Children[0]
	}
	tail.Children = append(tail.Children, hubcore.TreeNode{ID: "delegate", Kind: "subagent", Children: []hubcore.TreeNode{{ID: "delegate-original", Kind: "fork"}}})
	p, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "g", Revision: 1, Tree: tree})
	if err != nil {
		t.Fatal(err)
	}
	page, err := p.ArchivedList(navigationArchivedListRequest{Hint: navigationResourceArchivedProjects, ProjectKey: project.Key, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if nodes := navigationSummaryNodes(page.Sessions); nodes != maxNavigationDepth || page.NextCursor != "" || page.Total != 1 {
		t.Fatalf("depth page: nodes=%d cursor=%q total=%d", nodes, page.NextCursor, page.Total)
	}
	assertArchivedForkAccounting(t, page.Sessions, len(metas))
}

func TestArchivedForkNodeBudgetAccountsForOmittedOriginals(t *testing.T) {
	const sourceForks = maxNavigationNodes + 5
	children := make([]hubcore.TreeNode, sourceForks)
	for i := range children {
		children[i] = hubcore.TreeNode{ID: fmt.Sprintf("fork-%04d", i), Kind: "fork", State: "ended"}
	}
	// An activity branch after node admission has been exhausted is not an
	// omitted fork original.
	children = append(children, hubcore.TreeNode{ID: "delegate", Kind: "subagent", Children: []hubcore.TreeNode{{ID: "delegate-original", Kind: "fork"}}})
	p := archivedProjection(t, hubcore.TreeProject{Key: "project", Archived: []hubcore.TreeNode{{ID: "continuation", Kind: "session", State: "ended", Children: children}}})
	page, err := p.ArchivedList(navigationArchivedListRequest{Hint: navigationResourceProjects, ProjectKey: "project", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if nodes := navigationSummaryNodes(page.Sessions); nodes != maxNavigationNodes || page.NextCursor != "" || page.Total != 1 {
		t.Fatalf("node page: nodes=%d cursor=%q total=%d", nodes, page.NextCursor, page.Total)
	}
	assertArchivedForkAccounting(t, page.Sessions, 1+sourceForks)
}

// archivedCatalogFixture is one project key in each of the three catalogs,
// each with its own archived rows: 5 archived, 2 active, 3 test runs.
func archivedCatalogFixture() (whole, active, runs hubcore.TreeProject) {
	at := func(base int) func(int) time.Time {
		return func(i int) time.Time { return time.Unix(int64(base+i), 0).UTC() }
	}
	named := func(prefix string) func(int) string {
		return func(i int) string { return fmt.Sprintf("%s %d", prefix, i) }
	}
	active = hubcore.TreeProject{Key: "moving", Name: "moving", Archived: archivedRows("active", 2, at(10), named("active"))}
	whole = hubcore.TreeProject{Key: "moving", Name: "moving", IsArchived: true, Archived: archivedRows("whole", 5, at(20), named("whole"))}
	runs = hubcore.TreeProject{Key: "moving", Name: "moving", IsTestRun: true, Archived: archivedRows("run", 3, at(30), named("run"))}
	return whole, active, runs
}

// The catalog a client names is a hint. The same key can be in several
// catalogs, and hubcore's Tree moves a project between Projects and Archived
// projects as its sessions are archived and unarchived, so the hub reads the
// catalog that holds the key now and says which one it read.
func TestArchivedListReadsTheHintedCatalogWhenItHoldsTheKey(t *testing.T) {
	whole, active, runs := archivedCatalogFixture()
	p := archivedProjection(t, active, whole, runs)
	for _, tc := range []struct {
		catalog navigationResourceKind
		total   int
	}{{navigationResourceProjects, 2}, {navigationResourceArchivedProjects, 5}, {navigationResourceTestRuns, 3}} {
		page, err := p.ArchivedList(navigationArchivedListRequest{Hint: tc.catalog, ProjectKey: "moving"})
		if err != nil || page.Catalog != tc.catalog || page.Total != tc.total {
			t.Fatalf("hint %s: read %q total %d, err %v; want total %d", tc.catalog, page.Catalog, page.Total, err, tc.total)
		}
	}
}

func TestArchivedListFollowsAProjectToTheOtherMemberOfThePair(t *testing.T) {
	whole, active, _ := archivedCatalogFixture()
	for _, tc := range []struct {
		held    hubcore.TreeProject
		hint    navigationResourceKind
		want    navigationResourceKind
		wantLen int
	}{
		{whole, navigationResourceProjects, navigationResourceArchivedProjects, 5},
		{active, navigationResourceArchivedProjects, navigationResourceProjects, 2},
	} {
		page, err := archivedProjection(t, tc.held).ArchivedList(navigationArchivedListRequest{Hint: tc.hint, ProjectKey: "moving"})
		if err != nil || page.Catalog != tc.want || page.Total != tc.wantLen {
			t.Fatalf("hint %s: read %q total %d, err %v; want %s total %d", tc.hint, page.Catalog, page.Total, err, tc.want, tc.wantLen)
		}
	}
}

// A test run is no member of the pair: a hint to it reads no other catalog,
// and a hint to the pair never reads test runs.
func TestArchivedListTestRunsAreNoMemberOfThePair(t *testing.T) {
	whole, active, runs := archivedCatalogFixture()
	for _, tc := range []struct {
		held hubcore.TreeProject
		hint navigationResourceKind
	}{
		{active, navigationResourceTestRuns},
		{whole, navigationResourceTestRuns},
		{runs, navigationResourceProjects},
		{runs, navigationResourceArchivedProjects},
	} {
		page, err := archivedProjection(t, tc.held).ArchivedList(navigationArchivedListRequest{Hint: tc.hint, ProjectKey: "moving"})
		if err != nil || page.Catalog != "" || page.Total != 0 {
			t.Fatalf("hint %s: read %q total %d, err %v", tc.hint, page.Catalog, page.Total, err)
		}
	}
}

func TestArchivedListWithoutAHintReadsTheFirstCatalogHoldingTheKey(t *testing.T) {
	whole, active, runs := archivedCatalogFixture()
	for _, tc := range []struct {
		projects []hubcore.TreeProject
		want     navigationResourceKind
	}{
		{[]hubcore.TreeProject{active, whole, runs}, navigationResourceProjects},
		{[]hubcore.TreeProject{whole, runs}, navigationResourceArchivedProjects},
		{[]hubcore.TreeProject{runs}, navigationResourceTestRuns},
	} {
		page, err := archivedProjection(t, tc.projects...).ArchivedList(navigationArchivedListRequest{ProjectKey: "moving"})
		if err != nil || page.Catalog != tc.want {
			t.Fatalf("read %q, err %v; want %s", page.Catalog, err, tc.want)
		}
	}
}

// A cursor is bound to the hint the client sent, not to the catalog the hub
// read, so a list keeps paging when the two differ: with no hint, and with a
// hint to the pair member that no longer holds the project.
func TestArchivedListCursorIsBoundToTheHint(t *testing.T) {
	whole, _, _ := archivedCatalogFixture()
	p := archivedProjection(t, whole)
	want := make([]string, len(whole.Archived))
	for i, row := range whole.Archived {
		want[i] = row.ID
	}
	for _, hint := range []navigationResourceKind{"", navigationResourceProjects} {
		if ids, _ := pageAllArchived(t, p, hint, "moving", 2); strings.Join(ids, ",") != strings.Join(want, ",") {
			t.Fatalf("hint %q paged %v, want %v", hint, ids, want)
		}
	}

	first, err := p.ArchivedList(navigationArchivedListRequest{Hint: navigationResourceProjects, ProjectKey: "moving", Limit: 2})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page: cursor %q, err %v", first.NextCursor, err)
	}
	for _, hint := range []string{"", "archived_projects", "test_runs"} {
		_, err := parseNavigationArchivedListParams(appwire.ArchivedListParams{Catalog: hint, ProjectKey: "moving", Cursor: first.NextCursor})
		if err == nil || err.Error() != "cursor belongs to another archived list" {
			t.Fatalf("hint %q accepted a cursor minted under projects: %v", hint, err)
		}
	}
}

// A cursor continues its list by row order, so a project that moves between
// pages keeps paging from where it was, even when its rows changed meanwhile.
func TestArchivedListCursorContinuesAcrossAMoveBetweenPages(t *testing.T) {
	rows := archivedRows("moving", 4, func(i int) time.Time { return time.Unix(int64(100+i), 0).UTC() }, func(i int) string { return fmt.Sprintf("moving %d", i) })
	before := archivedProjection(t, hubcore.TreeProject{Key: "moving", Name: "moving", Archived: rows})
	first, err := before.ArchivedList(navigationArchivedListRequest{Hint: navigationResourceProjects, ProjectKey: "moving", Limit: 2})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page: cursor %q, err %v", first.NextCursor, err)
	}
	// Moved, with two newer rows archived and the cursor's own row gone, so
	// skipping the two rows already served would land elsewhere.
	newer := archivedRows("newer", 2, func(i int) time.Time { return time.Unix(int64(200+i), 0).UTC() }, func(i int) string { return fmt.Sprintf("newer %d", i) })
	moved := append(append(newer, rows[0]), rows[2:]...)
	after := archivedProjection(t, hubcore.TreeProject{Key: "moving", Name: "moving", IsArchived: true, Archived: moved})
	request, err := parseNavigationArchivedListParams(appwire.ArchivedListParams{Catalog: "projects", ProjectKey: "moving", Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	next, err := after.ArchivedList(request)
	if err != nil || next.Catalog != navigationResourceArchivedProjects || len(next.Sessions) != 2 {
		t.Fatalf("next page: read %q, %d rows, err %v", next.Catalog, len(next.Sessions), err)
	}
	if next.Sessions[0].SessionID != rows[2].ID || next.Sessions[1].SessionID != rows[3].ID {
		t.Fatalf("next page rows %s, %s; want %s, %s", next.Sessions[0].SessionID, next.Sessions[1].SessionID, rows[2].ID, rows[3].ID)
	}
}

// The response names the catalog it read, and a request may leave the
// catalog out.
func TestHubArchivedListSaysWhichCatalogItRead(t *testing.T) {
	server, key := archivedListServer(t, 3)
	for _, catalog := range []string{"", "projects", "archived_projects"} {
		response, err := dispatchArchivedList(t, server, appwire.ArchivedListParams{Catalog: catalog, ProjectKey: key})
		if err != nil || response.Catalog != "projects" || response.Total != 3 {
			t.Fatalf("hint %q: read %q total %d, err %v", catalog, response.Catalog, response.Total, err)
		}
	}
	raw, err := json.Marshal(appwire.ArchivedListResponse{Sessions: json.RawMessage("[]")})
	if err != nil || strings.Contains(string(raw), "catalog") {
		t.Fatalf("a response that read no catalog carries %s, err %v", raw, err)
	}
}

// Archived rows are no part of a project's navigation, but an archived
// session's location is, through its project, and the location carries the
// session's title. So renaming an archived session invalidates its project,
// and a client's archived list for that project follows that to the new title.
func TestRenamingAnArchivedSessionInvalidatesItsProject(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	archive := func(title string) {
		source.mu.Lock()
		defer source.mu.Unlock()
		source.inputs.Tree.Projects[0].Archived = []hubcore.TreeNode{{ID: aliasRootID, Title: title, Project: "p1", Kind: "session", State: "ended", UpdatedAt: now.Add(-30 * 24 * time.Hour)}}
		source.revision++
	}
	archive("before")
	service := newTestNavigationService(t, source)
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}

	archive("after")
	mutation, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasNavigationTarget(mutation.Targets, appwire.NavigationTargetProject, "p1") {
		t.Fatalf("rename targets = %+v, want project p1", mutation.Targets)
	}
}

// A key in several catalogs resolves to one of them for every read: the first
// of projects, archived projects and test runs holding it, as an archived list
// with no hint reads. So every session that catalog's project holds is located
// in the project a read of its key returns. Only an unresolved directory's
// "no-project" key can be in several catalogs.
func TestAKeyInSeveralCatalogsResolvesToOneForEveryRead(t *testing.T) {
	at := func(base int) func(int) time.Time {
		return func(i int) time.Time { return time.Unix(int64(base+i), 0).UTC() }
	}
	named := func(prefix string) func(int) string {
		return func(i int) string { return fmt.Sprintf("%s %d", prefix, i) }
	}
	const key = "no-project"
	active := hubcore.TreeProject{Key: key, Name: key, Recent: archivedRows("active", 2, at(10), named("active")), Archived: archivedRows("active-old", 1, at(1), named("active old"))}
	runs := hubcore.TreeProject{Key: key, Name: key, IsTestRun: true, Recent: archivedRows("run", 2, at(30), named("run")), Archived: archivedRows("run-old", 1, at(2), named("run old"))}
	p := archivedProjection(t, active, runs)

	for _, row := range active.Recent {
		location, ok := p.Location("local:" + row.ID)
		if !ok {
			t.Fatalf("%s has no location", row.ID)
		}
		project, ok := p.Project(location.ProjectKey)
		if !ok || !slices.ContainsFunc(project.Recent.Sessions, func(s hubapi.NavigationSessionSummary) bool { return s.SessionID == row.ID }) {
			t.Fatalf("%s is located in project %q, whose read does not hold it: %+v", row.ID, location.ProjectKey, project.Recent.Sessions)
		}
		page, err := p.ProjectPage(location.ProjectKey, location.Tier, 0, 10)
		if err != nil || !slices.ContainsFunc(page.Sessions, func(s hubapi.NavigationSessionSummary) bool { return s.SessionID == row.ID }) {
			t.Fatalf("%s is located in %s/%s, whose page does not hold it: %+v (%v)", row.ID, location.ProjectKey, location.Tier, page.Sessions, err)
		}
	}
	for _, row := range active.Archived {
		location, ok := p.Location("local:" + row.ID)
		if !ok {
			t.Fatalf("%s has no location", row.ID)
		}
		archived, err := p.ArchivedList(navigationArchivedListRequest{ProjectKey: location.ProjectKey})
		if err != nil || !slices.ContainsFunc(archived.Sessions, func(s hubapi.NavigationSessionSummary) bool { return s.SessionID == row.ID }) {
			t.Fatalf("%s is located in project %q, whose archived list does not hold it: %+v (%v)", row.ID, location.ProjectKey, archived.Sessions, err)
		}
	}
}

// The unhinted candidates are a copy, so a caller changing them cannot change
// the order every read of a key resolves in.
func TestArchivedListCandidatesDoNotShareTheCatalogOrder(t *testing.T) {
	candidates := archivedListCandidates("")
	candidates[0] = navigationResourceTestRuns
	if order := navigationCatalogOrder(); order[0] != navigationResourceProjects {
		t.Fatalf("changing the candidates changed navigationCatalogOrder: %v", order)
	}
}
