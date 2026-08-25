package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// --- hub_dashboard.go ---

// TestCovSelectedDashboardRow exercises row selection including empty/out-of-range.
func TestCovSelectedDashboardRow(t *testing.T) {
	// Out of range (foldedDashboardRows always includes launch row, so use high selected).
	m := hubModel{selected: 10}
	_, ok := m.selectedDashboardRow()
	if ok {
		t.Fatal("should return false for out-of-range selection")
	}

	// Negative.
	m = hubModel{selected: -1}
	_, ok = m.selectedDashboardRow()
	if ok {
		t.Fatal("should return false for negative selection")
	}

	// Valid selection (foldedDashboardRows always prepends a launch row).
	m = hubModel{selected: 0}
	row, ok := m.selectedDashboardRow()
	if !ok || row.kind != hubRowLaunch {
		t.Fatalf("row = %+v, ok = %v, want launch row", row, ok)
	}
}

// TestCovWorkingDirForProjectKey exercises working dir lookup.
func TestCovWorkingDirForProjectKey(t *testing.T) {
	// Empty key.
	m := hubModel{}
	if got := m.workingDirForProjectKey(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}

	// Found.
	m = hubModel{tree: hubTreeResponse{Projects: []hubTreeProject{
		{Key: "proj1", WorkingDir: "/tmp/proj1"},
	}}}
	if got := m.workingDirForProjectKey("proj1"); got != "/tmp/proj1" {
		t.Fatalf("got %q, want /tmp/proj1", got)
	}

	// Not found.
	if got := m.workingDirForProjectKey("unknown"); got != "" {
		t.Fatalf("got %q, want empty for unknown", got)
	}
}

// TestCovProjectKeyForSession exercises session project key resolution.
func TestCovProjectKeyForSession(t *testing.T) {
	// By ref matching a row.
	m := hubModel{
		detail: hubSessionDetail{Ref: "local:01TEST"},
		rows: []hubRow{
			{kind: hubRowSession, ref: appwire.Ref{SourceID: "local", ThreadID: "01TEST"}, projectKey: "proj1"},
		},
	}
	key, ok := m.projectKeyForSession()
	if !ok || key != "proj1" {
		t.Fatalf("key = %q, ok = %v, want proj1", key, ok)
	}

	// By working dir.
	m = hubModel{
		detail: hubSessionDetail{WorkingDir: "/tmp/proj1"},
		tree: hubTreeResponse{Projects: []hubTreeProject{
			{Key: "proj1", WorkingDir: "/tmp/proj1"},
		}},
	}
	key, ok = m.projectKeyForSession()
	if !ok || key != "proj1" {
		t.Fatalf("key = %q, ok = %v, want proj1", key, ok)
	}

	// No ref, no working dir.
	m = hubModel{}
	_, ok = m.projectKeyForSession()
	if ok {
		t.Fatal("should return false for empty detail")
	}

	// Ref but no matching row, no working dir.
	m = hubModel{detail: hubSessionDetail{Ref: "local:01TEST"}}
	_, ok = m.projectKeyForSession()
	if ok {
		t.Fatal("should return false for no matching row")
	}
}

// TestCovToggleDashboardProject exercises project toggle.
func TestCovToggleDashboardProject(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.toggleDashboardProject("proj1")
	if !m.dashboardProjectClosed["proj1"] {
		t.Fatal("should close project")
	}

	// Toggle again: opens.
	m.toggleDashboardProject("proj1")
	if m.dashboardProjectClosed["proj1"] {
		t.Fatal("should open project on second toggle")
	}

	// Empty key: no-op.
	m.toggleDashboardProject("")
	if len(m.dashboardProjectClosed) != 0 || m.dashboardProjectClosed[""] {
		t.Fatalf("empty toggle mutated closed map: %#v", m.dashboardProjectClosed)
	}
}

// TestCovSetDashboardProjectExpanded exercises explicit expand/collapse.
func TestCovSetDashboardProjectExpanded(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")

	// Expand (delete from closed).
	m.dashboardProjectClosed = map[string]bool{"proj1": true}
	m.setDashboardProjectExpanded("proj1", true)
	if m.dashboardProjectClosed["proj1"] {
		t.Fatal("should remove from closed on expand")
	}

	// Collapse.
	m.setDashboardProjectExpanded("proj1", false)
	if !m.dashboardProjectClosed["proj1"] {
		t.Fatal("should add to closed on collapse")
	}

	// Empty key: no-op.
	m.setDashboardProjectExpanded("", true)
	if len(m.dashboardProjectClosed) != 1 || !m.dashboardProjectClosed["proj1"] || m.dashboardProjectClosed[""] {
		t.Fatalf("empty expansion mutated closed map: %#v", m.dashboardProjectClosed)
	}
}

// TestCovToggleDashboardRecent exercises recent toggle.
func TestCovToggleDashboardRecent(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")

	m.toggleDashboardRecent("proj1")
	if !m.dashboardRecentOpen["proj1"] {
		t.Fatal("should open recent")
	}

	m.toggleDashboardRecent("proj1")
	if m.dashboardRecentOpen["proj1"] {
		t.Fatal("should close recent on second toggle")
	}

	// Empty: no-op.
	m.toggleDashboardRecent("")
	if len(m.dashboardRecentOpen) != 0 || m.dashboardRecentOpen[""] {
		t.Fatalf("empty recent toggle mutated open map: %#v", m.dashboardRecentOpen)
	}
}

// TestCovFocusDashboardProject exercises focusing a project.
func TestCovFocusDashboardProject(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.rows = []hubRow{
		{kind: hubRowLaunch, title: "Launch"},
		{kind: hubRowProject, projectKey: "proj1", groupKey: "proj1", title: "Proj1"},
	}
	m.focusDashboardProject("proj1")
	if m.mode != hubModeDashboard {
		t.Fatalf("mode = %v, want hubModeDashboard", m.mode)
	}
	if m.dashboardFilterActive {
		t.Fatal("filter should be deactivated")
	}
	if !m.dashboardProjectExpanded("proj1") {
		t.Fatal("project should be expanded")
	}
	if m.selected != 1 {
		t.Fatalf("selected = %d, want project row index 1", m.selected)
	}

	// Empty key: returns to dashboard.
	m.focusDashboardProject("")
	if m.mode != hubModeDashboard {
		t.Fatalf("mode = %v, want hubModeDashboard", m.mode)
	}
}

// TestCovSetSelectedDashboardProjectExpanded exercises selection-based expand.
func TestCovSetSelectedDashboardProjectExpanded(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.rows = []hubRow{
		{kind: hubRowLaunch},
		{kind: hubRowProject, projectKey: "proj1", groupKey: "proj1"},
	}
	m.selected = 1
	m.setSelectedDashboardProjectExpanded(m.rows, true)
	if m.dashboardProjectClosed["proj1"] {
		t.Fatal("should expand project")
	}

	// Empty rows: no-op.
	m.setSelectedDashboardProjectExpanded(nil, true)
	if len(m.dashboardProjectClosed) != 0 {
		t.Fatalf("empty rows mutated closed map: %#v", m.dashboardProjectClosed)
	}

	// Non-project row: no-op.
	m.selected = 0
	m.setSelectedDashboardProjectExpanded(m.rows, true)
	if len(m.dashboardProjectClosed) != 0 {
		t.Fatalf("launch row mutated closed map: %#v", m.dashboardProjectClosed)
	}

	// Empty projectKey: no-op.
	m.rows = []hubRow{
		{kind: hubRowLaunch},
		{kind: hubRowProject, projectKey: "", groupKey: "g1"},
	}
	m.selected = 1
	m.setSelectedDashboardProjectExpanded(m.rows, true)
	if len(m.dashboardProjectClosed) != 0 {
		t.Fatalf("empty project key mutated closed map: %#v", m.dashboardProjectClosed)
	}
}

// TestCovClampSelection exercises selection clamping.
func TestCovClampSelection(t *testing.T) {
	m := hubModel{selected: 5}
	m.clampSelection()
	if m.selected != 0 {
		t.Fatalf("selected = %d, want 0 for empty rows", m.selected)
	}

	// Within range: no change.
	m = hubModel{selected: 1, rows: []hubRow{{kind: hubRowLaunch}, {kind: hubRowProject}}}
	m.clampSelection()
	if m.selected != 1 {
		t.Fatalf("selected = %d, want 1 (within range)", m.selected)
	}

	// Too high: clamped.
	m = hubModel{selected: 10, rows: []hubRow{{kind: hubRowLaunch}, {kind: hubRowProject}}}
	m.clampSelection()
	if m.selected != 1 {
		t.Fatalf("selected = %d, want 1 (clamped)", m.selected)
	}

	// Negative: clamped to 0.
	m = hubModel{selected: -5, rows: []hubRow{{kind: hubRowLaunch}}}
	m.clampSelection()
	if m.selected != 0 {
		t.Fatalf("selected = %d, want 0 (clamped from negative)", m.selected)
	}
}

// TestCovFilterHubRows exercises row filtering.
func TestCovFilterHubRows(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowProject, title: "Proj1", project: "Proj1", groupKey: "g1"},
		{kind: hubRowSession, title: "Session A", project: "Proj1", groupKey: "g1", sourceLabel: "evener"},
		{kind: hubRowProject, title: "Proj2", project: "Proj2", groupKey: "g2"},
		{kind: hubRowSession, title: "Session B", project: "Proj2", groupKey: "g2", model: "gpt-5"},
	}

	// Filter by project name.
	filtered := filterHubRows(rows, "proj1")
	if len(filtered) != 2 || filtered[0].title != "Proj1" || filtered[1].title != "Session A" {
		t.Fatalf("filtered = %#v, want Proj1 and Session A", filtered)
	}

	// Filter by model.
	filtered = filterHubRows(rows, "gpt-5")
	if len(filtered) != 2 || filtered[0].title != "Proj2" || filtered[1].title != "Session B" {
		t.Fatalf("filtered = %#v, want Proj2 and Session B", filtered)
	}

	// Empty query: all rows.
	filtered = filterHubRows(rows, "")
	if len(filtered) != len(rows) {
		t.Fatalf("len = %d, want %d (all)", len(filtered), len(rows))
	}

	// No matches.
	filtered = filterHubRows(rows, "nonexistent")
	if len(filtered) != 0 {
		t.Fatalf("len = %d, want 0", len(filtered))
	}
}

// TestCovPresentationProjectGroupKey exercises grouping key.
func TestCovPresentationProjectGroupKey(t *testing.T) {
	// With identity.
	p := hubTreeProject{identity: "id1", Name: "Proj1"}
	if got := presentationProjectGroupKey(p); got != "presentation:id1" {
		t.Fatalf("got %q, want 'presentation:id1'", got)
	}

	// Without identity, with WorkingDir.
	p = hubTreeProject{WorkingDir: "/tmp/proj"}
	if got := presentationProjectGroupKey(p); got != "presentation:/tmp/proj" {
		t.Fatalf("got %q, want 'presentation:/tmp/proj'", got)
	}

	// Without identity or WorkingDir.
	p = hubTreeProject{}
	if got := presentationProjectGroupKey(p); got != "presentation:no-project" {
		t.Fatalf("got %q, want 'presentation:no-project'", got)
	}
}

// TestCovDashboardGroupKey exercises group key resolution.
func TestCovDashboardGroupKey(t *testing.T) {
	// From groupKey.
	row := hubRow{groupKey: "g1", projectKey: "p1"}
	if got := dashboardGroupKey(row); got != "g1" {
		t.Fatalf("got %q, want g1", got)
	}

	// Falls back to projectKey.
	row = hubRow{projectKey: "p1"}
	if got := dashboardGroupKey(row); got != "p1" {
		t.Fatalf("got %q, want p1", got)
	}
}

// TestCovBuildDashboardRows exercises row building from tree.
func TestCovBuildDashboardRows(t *testing.T) {
	// Empty tree.
	rows := buildDashboardRows(hubTreeResponse{})
	if len(rows) != 0 {
		t.Fatalf("len = %d, want 0 for empty tree", len(rows))
	}

	// Tree with projects and sessions.
	tree := hubTreeResponse{
		Projects: []hubTreeProject{
			{
				Key: "proj1", Name: "Proj1", WorkingDir: "/tmp/proj1",
				Sessions: []hubTreeNode{
					{Ref: "local:01A", Title: "Session A", State: "active", Live: true},
					{Ref: "local:01B", Title: "Session B", State: "closed"},
				},
			},
		},
		Live: []hubTreeNode{
			{Ref: "local:02C", Title: "Orphan", State: "active", Live: true},
		},
	}
	rows = buildDashboardRows(tree)
	if len(rows) != 5 {
		t.Fatalf("rows = %#v, want project, two project sessions, orphan group, and orphan", rows)
	}
	want := []struct {
		kind  hubRowKind
		title string
	}{
		{hubRowProject, "Proj1"},
		{hubRowSession, "Session A"},
		{hubRowSession, "Session B"},
		{hubRowProject, "(no project)"},
		{hubRowSession, "Orphan"},
	}
	for i, expected := range want {
		if rows[i].kind != expected.kind || rows[i].title != expected.title {
			t.Fatalf("row[%d] = %#v, want kind %v title %q", i, rows[i], expected.kind, expected.title)
		}
	}

	// With live sessions that have children.
	tree = hubTreeResponse{
		Projects: []hubTreeProject{
			{
				Key: "proj1", Name: "Proj1",
				Sessions: []hubTreeNode{
					{Ref: "local:01A", Title: "Parent", State: "active", Live: true,
						Children: []hubTreeNode{
							{Ref: "local:01D", Title: "Child", State: "idle", Live: true},
						}},
				},
			},
		},
	}
	rows = buildDashboardRows(tree)
	if len(rows) != 3 || rows[0].kind != hubRowProject || rows[0].title != "Proj1" || rows[1].title != "Parent" || rows[2].title != "Child" {
		t.Fatalf("rows = %#v, want project, parent, child", rows)
	}
}

// TestCovBuildProjectRows exercises project row building.
func TestCovBuildProjectRows(t *testing.T) {
	// With live and recent sessions.
	project := hubTreeProject{
		Key: "proj1", Name: "Proj1", WorkingDir: "/tmp/proj1",
		Sessions: []hubTreeNode{
			{Ref: "local:01A", Title: "Live", State: "active", Live: true},
			{Ref: "local:01B", Title: "Recent", State: "", Live: false},
		},
	}
	rows := buildProjectRows(project)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	// Live first.
	if !rows[0].live {
		t.Fatal("first row should be live")
	}

	// With empty key: uses presentation group key.
	project = hubTreeProject{
		Name: "Proj1", WorkingDir: "/tmp/proj1",
		Sessions: []hubTreeNode{
			{Ref: "local:01A", Title: "S1", State: "active", Live: true},
		},
	}
	rows = buildProjectRows(project)
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1", len(rows))
	}

	// Invalid ref: skipped.
	project = hubTreeProject{
		Key: "proj1", Name: "Proj1",
		Sessions: []hubTreeNode{
			{Ref: "invalid-ref", Title: "Bad"},
		},
	}
	rows = buildProjectRows(project)
	if len(rows) != 0 {
		t.Fatalf("len = %d, want 0 (invalid ref skipped)", len(rows))
	}
}

// TestCovFoldedDashboardRows exercises the folded row building.
func TestCovFoldedDashboardRows(t *testing.T) {
	m := hubModel{
		rows: []hubRow{
			{kind: hubRowProject, projectKey: "p1", groupKey: "g1", title: "Proj1", liveCount: 1, recentCount: 1},
			{kind: hubRowSession, title: "Live", groupKey: "g1", state: "active", live: true},
			{kind: hubRowSession, title: "Recent", groupKey: "g1", state: "closed"},
		},
		dashboardRecentOpen:    map[string]bool{},
		dashboardProjectClosed: map[string]bool{},
	}
	rows := m.foldedDashboardRows()
	// Closed recents are represented by a toggle, not the recent session.
	if len(rows) != 4 || rows[0].kind != hubRowLaunch || rows[1].kind != hubRowProject || rows[2].title != "Live" || rows[3].kind != hubRowRecentToggle {
		t.Fatalf("folded rows = %#v, want launch, project, live, recent toggle", rows)
	}

	// With collapsed project: skip sessions.
	m.dashboardProjectClosed = map[string]bool{"g1": true}
	rows = m.foldedDashboardRows()
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2 (launch + collapsed project)", len(rows))
	}

	// With recent open.
	m.dashboardProjectClosed = map[string]bool{}
	m.dashboardRecentOpen = map[string]bool{"g1": true}
	rows = m.foldedDashboardRows()
	if len(rows) != 5 || rows[4].kind != hubRowSession || rows[4].title != "Recent" {
		t.Fatalf("expanded rows = %#v, want recent session after toggle", rows)
	}
}

// TestCovDashboardRowLess exercises row comparison.
func TestCovDashboardRowLess(t *testing.T) {
	// Higher attention rank first.
	a := hubRow{state: "awaiting", title: "A", project: "P", updatedAt: 100}
	b := hubRow{state: "idle", title: "B", project: "P", updatedAt: 200}
	if !dashboardRowLess(a, b) {
		t.Fatal("awaiting should sort before idle")
	}

	// Same attention, more recent first.
	a = hubRow{state: "active", title: "A", project: "P", updatedAt: 200}
	b = hubRow{state: "active", title: "B", project: "P", updatedAt: 100}
	if !dashboardRowLess(a, b) {
		t.Fatal("more recent should sort first")
	}

	// Same attention, same recency: project name.
	a = hubRow{state: "active", title: "A", project: "Alpha", updatedAt: 100}
	b = hubRow{state: "active", title: "B", project: "Beta", updatedAt: 100}
	if !dashboardRowLess(a, b) {
		t.Fatal("Alpha should sort before Beta")
	}

	// Same attention, same recency, same project: title.
	a = hubRow{state: "active", title: "AAA", project: "P", updatedAt: 100}
	b = hubRow{state: "active", title: "BBB", project: "P", updatedAt: 100}
	if !dashboardRowLess(a, b) {
		t.Fatal("AAA should sort before BBB")
	}
}

// TestCovRowRecency exercises recency calculation.
func TestCovRowRecency(t *testing.T) {
	// UpdatedAt.
	row := hubRow{updatedAt: 200, createdAt: 100}
	if got := rowRecency(row); got != 200 {
		t.Fatalf("got %d, want 200", got)
	}

	// Falls back to createdAt.
	row = hubRow{createdAt: 100}
	if got := rowRecency(row); got != 100 {
		t.Fatalf("got %d, want 100", got)
	}

	// Both zero.
	row = hubRow{}
	if got := rowRecency(row); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

// --- hub_dashboard_view.go ---

// TestCovDashboardView exercises the dashboard view rendering.
func TestCovDashboardView(t *testing.T) {
	withTestColorProfile(t)
	m := newHubModel(nil, "http://hub.test")
	m.width = 100
	m.height = 40
	m.rows = []hubRow{
		{kind: hubRowLaunch, title: "Launch New Session", rowID: "dashboard:launch"},
	}
	got := m.dashboardView()
	if got == "" {
		t.Fatal("dashboardView should not be empty")
	}

	// With error.
	m.err = errors.New("test error")
	got = m.dashboardView()
	if !contains(got, "test error") {
		t.Fatal("dashboardView should show error")
	}

	// With filter active and no matches.
	m.err = nil
	m.dashboardFilterActive = true
	m.dashboardFilter.SetValue("nonexistent")
	got = m.dashboardView()
	if !contains(got, "No sessions match") {
		t.Fatal("dashboardView should show no-match message")
	}

	// Wide layout (>=120).
	m.dashboardFilterActive = false
	m.dashboardFilter.Reset()
	m.width = 140
	m.height = 40
	got = m.dashboardView()
	if got == "" {
		t.Fatal("wide dashboardView should not be empty")
	}

	// Narrow layout (<=72).
	m.width = 60
	got = m.dashboardView()
	if got == "" {
		t.Fatal("narrow dashboardView should not be empty")
	}
}

// TestCovDashboardRowWindow exercises window calculation.
func TestCovDashboardRowWindow(t *testing.T) {
	// Zero count.
	start, end := dashboardRowWindow(0, 0, 10)
	if start != 0 || end != 0 {
		t.Fatalf("start=%d end=%d, want 0 0", start, end)
	}

	// MaxRows >= count: full range.
	start, end = dashboardRowWindow(5, 2, 10)
	if start != 0 || end != 5 {
		t.Fatalf("start=%d end=%d, want 0 5", start, end)
	}

	// MaxRows < count, selected in middle.
	start, end = dashboardRowWindow(10, 5, 4)
	if end-start != 4 {
		t.Fatalf("window size = %d, want 4", end-start)
	}

	// Selected negative: clamped.
	start, end = dashboardRowWindow(10, -1, 4)
	if end-start != 4 {
		t.Fatalf("window size = %d, want 4", end-start)
	}

	// Selected >= count: clamped.
	start, end = dashboardRowWindow(10, 20, 4)
	if end-start != 4 {
		t.Fatalf("window size = %d, want 4", end-start)
	}

	// MaxRows 0: unbounded.
	start, end = dashboardRowWindow(10, 5, 0)
	if start != 0 || end != 10 {
		t.Fatalf("start=%d end=%d, want 0 10", start, end)
	}
}

// TestCovDashboardRowLimit exercises row limit calculation.
func TestCovDashboardRowLimit(t *testing.T) {
	// Zero height.
	if got := dashboardRowLimit(0, "top", "body", "footer"); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}

	// Normal height.
	got := dashboardRowLimit(40, "top\n", "body\n", "footer\n")
	if got < 1 {
		t.Fatalf("got %d, want >= 1", got)
	}
}

// TestCovPaletteOverlayHeight exercises overlay height calculation.
func TestCovPaletteOverlayHeight(t *testing.T) {
	// Zero height.
	if got := paletteOverlayHeight(0, "top", "body", "footer"); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}

	// Normal height.
	got := paletteOverlayHeight(40, "top\n", "body\n", "footer\n")
	if got < 1 {
		t.Fatalf("got %d, want >= 1", got)
	}
}

// TestCovDashboardRecentDetails exercises recent details rendering.
func TestCovDashboardRecentDetails(t *testing.T) {
	m := hubModel{tree: hubTreeResponse{Projects: []hubTreeProject{
		{Key: "p1", WorkingDir: "/tmp/proj1"},
	}}}
	row := hubRow{project: "Proj1", projectKey: "p1", recentCount: 3}
	got := m.dashboardRecentDetails(row)
	if !contains(got, "Proj1") || !contains(got, "Recent:   3") {
		t.Fatalf("got %q, want to contain 'Proj1' and 'Recent:   3'", got)
	}

	// No working dir.
	m = hubModel{}
	got = m.dashboardRecentDetails(row)
	if !contains(got, "Proj1") {
		t.Fatalf("got %q, want to contain 'Proj1'", got)
	}
}

// TestCovDashboardSessionDetails exercises session details rendering.
func TestCovDashboardSessionDetails(t *testing.T) {
	row := hubRow{
		ref:         appwire.Ref{SourceID: "local", ThreadID: "01TEST"},
		title:       "My Session",
		project:     "Proj1",
		sourceLabel: "evener",
		state:       "active",
		model:       "gpt-5",
		age:         "5m ago",
	}
	got := dashboardSessionDetails(row)
	for _, want := range []string{"01TEST", "My Session", "Proj1", "evener", "active", "gpt-5", "5m ago"} {
		if !contains(got, want) {
			t.Fatalf("got %q, missing %q", got, want)
		}
	}

	// Minimal row (no title, no ref).
	row = hubRow{}
	got = dashboardSessionDetails(row)
	if !contains(got, "Session:") {
		t.Fatalf("got %q, want 'Session:'", got)
	}
}

// TestCovStatusDot exercises status dot rendering.
func TestCovStatusDot(t *testing.T) {
	for _, state := range []string{"awaiting", "active", "warning", "idle", "errored", "systemError"} {
		if got := statusDot(state); got != "●" {
			t.Fatalf("statusDot(%q) = %q, want ●", state, got)
		}
	}
	// Default.
	if got := statusDot("unknown"); got != "○" {
		t.Fatalf("statusDot(unknown) = %q, want ○", got)
	}
}

// TestCovProjectSummary exercises project summary rendering.
func TestCovProjectSummary(t *testing.T) {
	project := hubRow{kind: hubRowProject, project: "Proj1", groupKey: "g1", state: "active", liveCount: 2, recentCount: 1}
	rows := []hubRow{
		project,
		{kind: hubRowSession, groupKey: "g1", state: "active", live: true},
		{kind: hubRowSession, groupKey: "g1", state: "closed"},
	}
	got := projectSummary(project, rows)
	if !contains(got, "2 live") || !contains(got, "1 recent") {
		t.Fatalf("got %q, want '2 live' and '1 recent'", got)
	}

	// No recent.
	project = hubRow{kind: hubRowProject, project: "Proj1", groupKey: "g1", state: "active", liveCount: 1, recentCount: 0}
	rows = []hubRow{project, {kind: hubRowSession, groupKey: "g1", state: "active", live: true}}
	got = projectSummary(project, rows)
	if contains(got, "recent") {
		t.Fatalf("got %q, should not contain 'recent'", got)
	}
	if !contains(got, "1 live") {
		t.Fatalf("got %q, want '1 live'", got)
	}
}

// TestCovProjectSessionCounts exercises session counting.
func TestCovProjectSessionCounts(t *testing.T) {
	project := hubRow{kind: hubRowProject, groupKey: "g1", liveCount: 2, recentCount: 1}
	rows := []hubRow{
		{kind: hubRowSession, groupKey: "g1", state: "active", live: true},
		{kind: hubRowSession, groupKey: "g1", state: "closed"},
		{kind: hubRowSession, groupKey: "g2", state: "active", live: true},
	}
	live, recent := projectSessionCounts(project, rows)
	if live != 2 || recent != 1 {
		t.Fatalf("live=%d recent=%d, want 2 1", live, recent)
	}

	// Without pre-computed counts.
	project = hubRow{kind: hubRowProject, groupKey: "g1", liveCount: 0, recentCount: 0}
	live, recent = projectSessionCounts(project, rows)
	if live != 1 || recent != 1 {
		t.Fatalf("live=%d recent=%d, want 1 1", live, recent)
	}
}

// TestCovDashboardTitle exercises title extraction.
func TestCovDashboardTitle(t *testing.T) {
	if got := dashboardTitle("Hello\nWorld"); got != "Hello" {
		t.Fatalf("got %q, want 'Hello' (first line)", got)
	}

	if got := dashboardTitle("  spaced  "); got != "spaced" {
		t.Fatalf("got %q, want 'spaced'", got)
	}

	// Empty lines only.
	if got := dashboardTitle("\n\n"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestCovDashboardCell exercises cell formatting.
func TestCovDashboardCell(t *testing.T) {
	if got := dashboardCell("  hello  world  "); got != "hello world" {
		t.Fatalf("got %q, want 'hello world'", got)
	}
	if got := dashboardCell(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestCovDashboardRecentExpanded exercises recent expansion check.
func TestCovDashboardRecentExpanded(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowRecentToggle, groupKey: "g1"},
		{kind: hubRowSession, groupKey: "g1", state: "closed"},
	}
	if !dashboardRecentExpanded(rows, 0) {
		t.Fatal("should be expanded when followed by ended session")
	}

	// Not expanded (no following session).
	rows = []hubRow{
		{kind: hubRowRecentToggle, groupKey: "g1"},
		{kind: hubRowProject},
	}
	if dashboardRecentExpanded(rows, 0) {
		t.Fatal("should not be expanded when followed by project")
	}

	// Out of range.
	if dashboardRecentExpanded(rows, -1) {
		t.Fatal("should be false for negative index")
	}
	if dashboardRecentExpanded(rows, 10) {
		t.Fatal("should be false for out-of-range index")
	}
}

// TestCovDashboardProjectExpanded exercises project expansion check.
func TestCovDashboardProjectExpanded(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowProject, groupKey: "g1", liveCount: 1},
		{kind: hubRowSession, groupKey: "g1"},
	}
	if !dashboardProjectExpanded(rows, 0) {
		t.Fatal("should be expanded when followed by session")
	}

	// Not expanded (followed by another project).
	rows = []hubRow{
		{kind: hubRowProject, groupKey: "g1"},
		{kind: hubRowProject, groupKey: "g2"},
	}
	if dashboardProjectExpanded(rows, 0) {
		t.Fatal("should not be expanded when followed by another project")
	}

	// Empty project with no children: expanded by default.
	rows = []hubRow{
		{kind: hubRowProject, groupKey: "g1", liveCount: 0, recentCount: 0},
	}
	if !dashboardProjectExpanded(rows, 0) {
		t.Fatal("should be expanded for empty project")
	}

	// Out of range.
	if dashboardProjectExpanded(rows, -1) {
		t.Fatal("should be false for negative index")
	}
}

// ensure imports are used
var _ = transcript.MsgSystem
var _ tea.Msg = hubTreeMsg{}
