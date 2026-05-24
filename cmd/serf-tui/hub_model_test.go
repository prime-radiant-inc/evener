package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

func TestHubModelInitialFetchRendersLiveAndRecentRows(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
			return threadListResponse(hubTreeResponse{
				Live: []hubTreeNode{{
					Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "awaiting", Project: "serf", Live: true,
				}},
				Projects: []hubTreeProject{{
					Name: "serf",
					Sessions: []hubTreeNode{{
						Ref: "local:01PAST", SessionID: "01PAST", Title: "past task", State: "ended", Project: "serf",
					}},
				}},
			}), nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	msg := fetchHubTree(client)()
	updated, _ := m.Update(msg)
	got := updated.(hubModel).View()
	for _, want := range []string{"Launch New Session", "live task", "awaiting", "serf", "1 recent"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "past task") {
		t.Fatalf("dashboard should fold ended sessions by default:\n%s", got)
	}
}

func TestHubModelDashboardShowsFullSessionTreeGroupedByProject(t *testing.T) {
	tree := hubTreeResponse{
		Live: []hubTreeNode{
			{Ref: "local:01LIVEA", SessionID: "01LIVEA", Title: "live alpha", State: "awaiting", Project: "serf", Live: true},
			{Ref: "local:01LIVEB", SessionID: "01LIVEB", Title: "live beta", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01BRAIN", SessionID: "01BRAIN", Title: "brain live", State: "active", Project: "brainstorm", Live: true},
		},
		Projects: []hubTreeProject{
			{
				Key:         "serf",
				Name:        "serf",
				RollupState: "awaiting",
				Sessions: []hubTreeNode{
					{Ref: "local:01LIVEA", SessionID: "01LIVEA", Title: "live alpha", State: "awaiting", Project: "serf", Live: true},
					{Ref: "local:01LIVEB", SessionID: "01LIVEB", Title: "live beta", State: "idle", Project: "serf", Live: true},
					{Ref: "local:01ENDED", SessionID: "01ENDED", Title: "ended history", State: "ended", Project: "serf", Live: false},
				},
			},
			{
				Key:         "brainstorm",
				Name:        "brainstorm",
				RollupState: "active",
				Sessions: []hubTreeNode{
					{Ref: "local:01BRAIN", SessionID: "01BRAIN", Title: "brain live", State: "active", Project: "brainstorm", Live: true},
				},
			},
			{
				Key:  "archive",
				Name: "archive",
				Sessions: []hubTreeNode{
					{Ref: "local:01ARCHIVE", SessionID: "01ARCHIVE", Title: "archived task", State: "ended", Project: "archive", Live: false},
				},
			},
		},
	}

	rows := buildDashboardRows(tree)
	if len(rows) != 8 {
		t.Fatalf("rows=%d: %+v", len(rows), rows)
	}
	if rows[0].kind != hubRowProject || rows[0].project != "serf" {
		t.Fatalf("first row=%+v, want serf project header", rows[0])
	}
	if rows[1].kind != hubRowSession || rows[1].title != "live alpha" {
		t.Fatalf("second row=%+v, want live alpha session", rows[1])
	}
	if rows[3].kind != hubRowSession || rows[3].title != "ended history" {
		t.Fatalf("fourth row=%+v, want ended history session", rows[3])
	}
	if rows[4].kind != hubRowProject || rows[4].project != "brainstorm" {
		t.Fatalf("fifth row=%+v, want brainstorm project header", rows[4])
	}
	if rows[6].kind != hubRowProject || rows[6].project != "archive" {
		t.Fatalf("seventh row=%+v, want archive project header", rows[6])
	}
	if rows[7].kind != hubRowSession || rows[7].title != "archived task" {
		t.Fatalf("eighth row=%+v, want archived task session", rows[7])
	}

	m := newHubModel(nil, "http://hub.test")
	m.tree = tree
	m.rows = rows
	got := m.dashboardView()
	for _, want := range []string{"Launch New Session", "serf", "live alpha", "live beta", "brainstorm", "brain live", "archive", "1 recent"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, got)
		}
	}
	for _, folded := range []string{"ended history", "archived task"} {
		if strings.Contains(got, folded) {
			t.Fatalf("dashboard should fold ended session %q by default:\n%s", folded, got)
		}
	}
	m.selected = 4
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	expanded := updated.(hubModel).dashboardView()
	if !strings.Contains(expanded, "ended history") {
		t.Fatalf("dashboard disclosure did not expand ended history:\n%s", expanded)
	}
}

func TestHubModelDashboardRendersProjectTreeHierarchy(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.width = 100
	m.tree = hubTreeResponse{
		Projects: []hubTreeProject{{
			Key:         "serf",
			Name:        "serf",
			RollupState: "idle",
			Sessions: []hubTreeNode{
				{Ref: "local:01ALPHA", SessionID: "01ALPHA", Title: "alpha task", State: "idle", Project: "serf", SourceLabel: "local", Model: "gpt-5", Live: true, UpdatedAt: 20},
				{Ref: "codex-local:01BETA", SessionID: "01BETA", Title: "beta task", State: "active", Project: "serf", SourceLabel: "codex-local", Model: "gpt-5.3-codex", Live: true, UpdatedAt: 10},
			},
		}, {
			Key:         "codex",
			Name:        "codex",
			RollupState: "idle",
			Sessions: []hubTreeNode{
				{Ref: "codex:01GAMMA", SessionID: "01GAMMA", Title: "gamma task", State: "idle", Project: "codex", SourceLabel: "codex", Model: "gpt-5.3-codex", Live: true, UpdatedAt: 5},
			},
		}},
	}
	m.rows = buildDashboardRows(m.tree)

	got := m.dashboardView()
	for _, want := range []string{"Launch New Session", "▾", "▍", "alpha task", "codex-local"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard tree missing %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"├─", "└─"} {
		if strings.Contains(got, bad) {
			t.Fatalf("dashboard tree should not contain tree connector %q:\n%s", bad, got)
		}
	}
}

func TestHubModelDashboardLaunchRowOpensUnscopedSpawn(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "serf",
		Name:       "serf",
		WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(hubModel)
	if got.mode != hubModeSpawn {
		t.Fatalf("launch row did not open spawn mode: %v", got.mode)
	}
	if got.spawnDir != "" || got.spawnProject != "" {
		t.Fatalf("global launch row should be unscoped, dir=%q project=%q", got.spawnDir, got.spawnProject)
	}
}

func TestHubModelDashboardLaunchSelectionSurvivesTreeRefresh(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.selected = 0
	tree := hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "serf",
		Name:       "serf",
		WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}

	updated, _ := m.Update(hubTreeMsg{tree: tree})
	m = updated.(hubModel)
	if m.selected != 1 {
		t.Fatalf("initial dashboard selection=%d, want first project row", m.selected)
	}

	m.selected = 0
	updated, _ = m.Update(hubTreeMsg{tree: tree})
	m = updated.(hubModel)
	if m.selected != 0 {
		t.Fatalf("refresh should keep launch row selected, got selected=%d", m.selected)
	}
}

func TestHubModelDashboardNewFromProjectRowUsesProjectDir(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "serf",
		Name:       "serf",
		WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	got := updated.(hubModel)
	if got.mode != hubModeSpawn {
		t.Fatalf("new key did not open spawn mode: %v", got.mode)
	}
	if got.spawnDir != "/tmp/serf" || got.spawnProject != "serf" {
		t.Fatalf("project row launch should inherit project scope, dir=%q project=%q", got.spawnDir, got.spawnProject)
	}
}

func TestHubModelDashboardClampsRowsToTerminalHeight(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.width = 80
	m.height = 12
	project := hubTreeProject{
		Key:  "implementation-swift-iris-velvet-task-0",
		Name: "implementation-swift-iris-velvet-task-0",
	}
	for i := range 20 {
		project.Sessions = append(project.Sessions, hubTreeNode{
			Ref:       fmt.Sprintf("local:01TASK%02d", i),
			SessionID: fmt.Sprintf("01TASK%02d", i),
			Title:     fmt.Sprintf("task %02d", i),
			State:     "ended",
			Project:   project.Name,
		})
	}
	m.tree = hubTreeResponse{Projects: []hubTreeProject{project}}
	m.rows = buildDashboardRows(m.tree)
	m.dashboardRecentOpen = map[string]bool{project.Key: true}
	m.selected = len(m.dashboardRows()) - 1

	got := m.dashboardView()
	if lines := shellSectionLineCount(got); lines > m.height {
		t.Fatalf("dashboard rendered %d lines, want <= %d:\n%s", lines, m.height, got)
	}
	if !strings.Contains(got, "task 19") {
		t.Fatalf("selected row was scrolled out of bounded dashboard:\n%s", got)
	}
	if strings.Contains(got, "task 00") {
		t.Fatalf("bounded dashboard still rendered rows outside the visible window:\n%s", got)
	}
}

func TestRenderDashboardRowsKeepsLongPromptsSingleLineAndProjectVisible(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowProject, project: "implementation-swift-iris-velvet-task-0", projectKey: "implementation-swift-iris-velvet-task-0", state: "ended"},
		{
			kind:        hubRowSession,
			ref:         appwire.Ref{SourceID: "local", ThreadID: "01TASK"},
			sourceLabel: "serf",
			title:       "# Debugger\n\nFind the root cause before proposing any fix. You do not implement fixes.",
			project:     "implementation-swift-iris-velvet-task-0",
			projectKey:  "implementation-swift-iris-velvet-task-0",
			state:       "ended",
			age:         "2d",
		},
	}

	got := ansiPattern.ReplaceAllString(renderDashboardRows(rows, 1, 80, false), "")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("dashboard rows should be one physical line per row, got %d lines:\n%s", len(lines), got)
	}
	if !strings.Contains(lines[1], "implementation-swift-iris-velvet-task-0") {
		t.Fatalf("session row missing project label:\n%s", lines[1])
	}
	if !strings.Contains(lines[1], "# Debugger") || strings.Contains(lines[1], "Find the root cause") {
		t.Fatalf("session row should show a truncated single-line title, got:\n%s", lines[1])
	}
	for _, line := range lines {
		if lipgloss.Width(line) > 80 {
			t.Fatalf("line width=%d, want <= 80: %q", lipgloss.Width(line), line)
		}
	}
}

func TestHubModelDashboardSelectedRowUsesVisualStyle(t *testing.T) {
	withTestColorProfile(t)
	m := sampleHubModel(100)
	m.selected = 0

	got := m.dashboardView()
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("dashboard should use terminal styling for selected rows and section hierarchy:\n%q", got)
	}
}

func TestHubModelDashboardSelectedTreeRowsDoNotMixCursorAndBranchGlyphs(t *testing.T) {
	m := sampleHubModel(100)
	rows := m.dashboardRows()

	got := renderDashboardRows(rows, 0, 100, false) + renderDashboardRows(rows, 1, 100, false)
	plain := ansiPattern.ReplaceAllString(got, "")
	for _, bad := range []string{">▾", ">├", ">└"} {
		if strings.Contains(plain, bad) {
			t.Fatalf("selected dashboard rows should not combine cursor and tree glyph %q:\n%s", bad, plain)
		}
	}
}

func TestHubModelDashboardDetailsRendersAsPane(t *testing.T) {
	withTestColorProfile(t)
	m := sampleHubModel(120)
	rows := m.dashboardRows()

	got := m.dashboardDetailsView(rows, 48)
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("dashboard details should render with pane styling:\n%q", got)
	}
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "  details") {
		t.Fatalf("dashboard details should be offset inside the pane:\n%q", plain)
	}
}

func TestHubModelDashboardSortsByAttentionThenRecency(t *testing.T) {
	tree := hubTreeFromThreads([]appwire.Thread{
		{
			ID:            "01IDLE",
			SessionID:     "01IDLE",
			Name:          "idle recent",
			ModelProvider: "gpt-5",
			CWD:           "/repo/serf",
			Source:        "local",
			UpdatedAt:     300,
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:          appwire.SerfThread{Ref: "local:01IDLE"},
		},
		{
			ID:            "01OPSOLD",
			SessionID:     "01OPSOLD",
			Name:          "ops awaiting old",
			ModelProvider: "gpt-5",
			CWD:           "/repo/ops",
			Source:        "local",
			UpdatedAt:     100,
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
			Serf:          appwire.SerfThread{Ref: "local:01OPSOLD"},
		},
		{
			ID:            "01BRAIN",
			SessionID:     "01BRAIN",
			Name:          "brain awaiting",
			ModelProvider: "gpt-5",
			CWD:           "/repo/brainstorm",
			Source:        "local",
			UpdatedAt:     400,
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
			Serf:          appwire.SerfThread{Ref: "local:01BRAIN"},
		},
		{
			ID:            "01OPSNEW",
			SessionID:     "01OPSNEW",
			Name:          "ops awaiting new",
			ModelProvider: "gpt-5",
			CWD:           "/repo/ops",
			Source:        "local",
			UpdatedAt:     500,
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
			Serf:          appwire.SerfThread{Ref: "local:01OPSNEW"},
		},
	})

	rows := buildDashboardRows(tree)
	got := dashboardRowLabels(rows)
	want := []string{
		"project:ops",
		"session:ops awaiting new",
		"session:ops awaiting old",
		"project:brainstorm",
		"session:brain awaiting",
		"project:serf",
		"session:idle recent",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("dashboard order:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestHubModelDashboardShowsSourceLabels(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Live: []hubTreeNode{{
		Ref: "codex-local:01LIVE", SessionID: "01LIVE", Title: "external task", State: "idle", Project: "serf", Model: "openai", Live: true,
	}}}
	m.rows = buildDashboardRows(m.tree)

	got := m.dashboardView()
	if !strings.Contains(got, "codex-local") {
		t.Fatalf("dashboard missing source label:\n%s", got)
	}
}

func TestHubModelDashboardNarrowUsesOneColumnWithEllipses(t *testing.T) {
	m := newHubModel(nil, "http://hub.test/with/a/very/long/url/that/must/not/blow/out/the/header")
	m.width = 60
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "very-long-project",
		Name: "very-long-project-name-that-needs-to-fit",
		Sessions: []hubTreeNode{{
			Ref:       "local:01LONG",
			SessionID: "01LONG",
			Title:     "this dashboard session title is long enough to require truncation",
			State:     "awaiting",
			Project:   "very-long-project-name-that-needs-to-fit",
			Model:     "openai/gpt-5-with-a-long-suffix",
			Age:       "42m",
			Live:      true,
		}},
	}}}
	m.rows = buildDashboardRows(m.tree)

	got := m.dashboardView()
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("narrow dashboard line width=%d want <=%d:\n%s\n\nfull view:\n%s", lipgloss.Width(line), m.width, line, got)
		}
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("narrow dashboard did not use ellipses:\n%s", got)
	}
	if strings.Contains(got, "details") {
		t.Fatalf("narrow dashboard should stay one-column without details drawer:\n%s", got)
	}
	if !strings.Contains(got, "dashboard") {
		t.Fatalf("narrow dashboard should keep the dashboard shortcut explicit:\n%s", got)
	}
}

func TestHubModelDashboardWideDetailsFollowSelection(t *testing.T) {
	m := sampleHubModel(140)
	m.selected = 1

	projectView := m.dashboardView()
	for _, want := range []string{"details", "Project:  serf", "Live:     2", "Dir:      /Users/jesse/Documents/GitHub/prime-radiant-inc/serf"} {
		if !strings.Contains(projectView, want) {
			t.Fatalf("wide dashboard project details missing %q:\n%s", want, projectView)
		}
	}

	m.selected = dashboardRowIndex(m.dashboardRows(), "Restore hub TUI widgets")
	sessionView := m.dashboardView()
	for _, want := range []string{"details", "Session:  01SERF", "Title:    Restore hub TUI widgets", "Ref:      local:01SERF"} {
		if !strings.Contains(sessionView, want) {
			t.Fatalf("wide dashboard session details missing %q:\n%s", want, sessionView)
		}
	}
	if strings.Contains(sessionView, "Live:     2") {
		t.Fatalf("wide details did not update after selection change:\n%s", sessionView)
	}
}

func TestHubModelDashboardWideDrawerDoesNotStealTextInput(t *testing.T) {
	m := sampleHubModel(140)
	if view := m.dashboardView(); !strings.Contains(view, "details") {
		t.Fatalf("wide dashboard missing details drawer before input checks:\n%s", view)
	}

	m.enterHubFilter()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(hubModel)
	if m.mode == hubModeSpawn || m.dashboardFilter.Value() != "n" {
		t.Fatalf("filter did not own printable n key: mode=%v filter=%q", m.mode, m.dashboardFilter.Value())
	}

	m.dashboardFilter.Reset()
	m.dashboardFilterActive = false
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(hubModel)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd != nil {
		t.Fatal("palette-owned printable key should not run a command")
	}
	m = updated.(hubModel)
	if m.mode == hubModeSpawn || m.commandPalette == nil || m.commandPalette.panel.filter != "n" {
		t.Fatalf("palette did not own printable n key: mode=%v palette=%+v", m.mode, m.commandPalette)
	}
}

func TestHubModelDashboardWideDetailsShowDiagnosticSelection(t *testing.T) {
	m := sampleHubModel(140)
	m.err = fmt.Errorf("hub connection lost")

	got := m.dashboardView()
	for _, want := range []string{"Diagnostic", "hub connection lost", "Next:     refresh dashboard or check Hub health"} {
		if !strings.Contains(got, want) {
			t.Fatalf("wide dashboard diagnostic details missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelDashboardUsesNForNewSession(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key: "serf", Name: "serf", WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true}},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd != nil {
		t.Fatal("legacy s key should not start spawn")
	}
	if got := updated.(hubModel); got.mode == hubModeSpawn {
		t.Fatal("legacy s key opened spawn; n is the approved new-session key")
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd != nil {
		t.Fatal("opening dashboard spawn without a client should be synchronous")
	}
	if got := updated.(hubModel); got.mode != hubModeSpawn || got.spawnDir != "/tmp/serf" {
		t.Fatalf("n key did not open spawn with selected project dir: mode=%v dir=%q", got.mode, got.spawnDir)
	}
}

func TestHubModelDashboardSlashOpensCommandPalette(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01ALPHA", SessionID: "01ALPHA", Title: "alpha task", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01BETA", SessionID: "01BETA", Title: "beta task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if cmd != nil {
		t.Fatal("opening dashboard palette should be synchronous")
	}
	m = updated.(hubModel)
	for _, r := range "beta" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(hubModel)
	}

	if m.commandPalette == nil {
		t.Fatal("dashboard slash did not open command palette")
	}
	view := m.dashboardView()
	if !strings.Contains(view, "Command palette") || !strings.Contains(view, "Filter: beta") || !strings.Contains(view, "beta task") {
		t.Fatalf("filtered dashboard missing active filter/beta row:\n%s", view)
	}
	if strings.Contains(view, "alpha task") {
		t.Fatalf("dashboard palette still shows alpha row after filtering beta:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(hubModel)
	if m.commandPalette != nil {
		t.Fatalf("palette after esc=%+v, want closed", m.commandPalette)
	}
	if view := m.dashboardView(); !strings.Contains(view, "alpha task") || !strings.Contains(view, "beta task") {
		t.Fatalf("dashboard did not restore rows after closing palette:\n%s", view)
	}
}

func TestBuildProjectRowsShowsLiveThenRecent(t *testing.T) {
	project := hubTreeProject{
		Key:         "serf",
		Name:        "serf",
		RollupState: "awaiting",
		Sessions: []hubTreeNode{
			{Ref: "local:01ENDED", SessionID: "01ENDED", Title: "ended history", State: "ended", Project: "serf", Live: false},
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "awaiting", Project: "serf", Live: true},
		},
	}

	rows := buildProjectRows(project)
	if len(rows) != 2 {
		t.Fatalf("rows=%d: %+v", len(rows), rows)
	}
	if rows[0].title != "live task" || !rows[0].live {
		t.Fatalf("first row=%+v, want live task", rows[0])
	}
	if rows[1].title != "ended history" || rows[1].live {
		t.Fatalf("second row=%+v, want ended history", rows[1])
	}
}

func TestHubModelEndedSessionCanResumeOnSend(t *testing.T) {
	var gotStart appwire.TurnStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			gotStart = params
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_resumed"}}, nil
		})
	})
	defer cleanup()

	detail := hubDetailFromThread(appwireThread(hubTreeNode{
		Ref:       "local:01ENDED",
		SessionID: "01ENDED",
		Title:     "ended history",
		State:     appwire.ThreadStatusNotLoaded,
		Project:   "serf",
		Live:      false,
	}, "/tmp/serf"))
	if detail.Live {
		t.Fatal("ended thread detail reported live")
	}
	if detail.Capabilities.Send || detail.Capabilities.Steer || detail.Capabilities.Interrupt || detail.Capabilities.Compact || detail.Capabilities.Clear || detail.Capabilities.Shutdown || detail.Capabilities.ChangeModel {
		t.Fatalf("ended thread kept write/action capabilities: %+v", detail.Capabilities)
	}
	if !detail.Capabilities.Resume {
		t.Fatalf("ended thread should advertise resume: %+v", detail.Capabilities)
	}

	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSession
	m.detail = detail
	m.session = newModel("", "", nil)
	m.session.messages = []chatMessage{{Kind: msgAssistant, Text: "finished transcript"}}

	got := m.sessionView()
	for _, unwanted := range []string{"read-only", "source does not support send"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("ended resumable session should not render %q:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "enter: send") {
		t.Fatalf("ended resumable session should accept a new message:\n%s", got)
	}

	m.session.setInputValue("restart with this")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("sending to ended resumable session returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	gotModel := updated.(hubModel)
	if gotStart.Ref != "local:01ENDED" || testInputText(gotStart.Input) != "restart with this" {
		t.Fatalf("turn start params=%+v", gotStart)
	}
	if gotModel.session.input.Value() != "" {
		t.Fatalf("input not reset after resume-send: %q", gotModel.session.input.Value())
	}
}

func TestHubModelCommandPaletteOwnsPrintableKeys(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = newHubTUISampleCorpus().DashboardTree
	m.rows = buildDashboardRows(m.tree)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(hubModel)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd != nil {
		t.Fatal("palette-owned printable key should not run global new-session action")
	}
	m = updated.(hubModel)
	if m.mode == hubModeSpawn {
		t.Fatal("palette-owned n key opened spawn")
	}
	if m.commandPalette == nil || m.commandPalette.panel.filter != "n" {
		t.Fatalf("palette did not own printable n key: %+v", m.commandPalette)
	}
}

func TestHubModelCommandPaletteCanOpenNewSession(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = newHubTUISampleCorpus().DashboardTree
	m.rows = buildDashboardRows(m.tree)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(hubModel)
	for _, r := range "new" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(hubModel)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("opening spawn from command palette without a client should be synchronous")
	}
	got := updated.(hubModel)
	if got.commandPalette != nil || got.mode != hubModeSpawn {
		t.Fatalf("palette did not open spawn: mode=%v palette=%+v", got.mode, got.commandPalette)
	}
}

func TestHubModelDashboardProjectHeaderTogglesChildren(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("project header enter should not fetch a session")
	}
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
	if strings.Contains(got.dashboardView(), "live task") {
		t.Fatalf("collapsed project still rendered child session:\n%s", got.dashboardView())
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRight})
	got = updated.(hubModel)
	if !strings.Contains(got.dashboardView(), "live task") {
		t.Fatalf("right arrow did not expand project children:\n%s", got.dashboardView())
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyLeft})
	got = updated.(hubModel)
	if strings.Contains(got.dashboardView(), "live task") {
		t.Fatalf("left arrow did not collapse project children:\n%s", got.dashboardView())
	}
}

func TestHubModelSessionEscEntersBrowseInsteadOfDashboard(t *testing.T) {
	m := newSessionHubModel(nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(hubModel)
	if got.mode != hubModeSession {
		t.Fatalf("mode=%v, want session", got.mode)
	}
	if !got.session.scrollMode {
		t.Fatal("esc should enter browse focus")
	}
}

func TestHubModelBrowseSelectionHighlightsSelectedMessage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 100
	m.session.width = 100
	m.session.messages = []chatMessage{
		{Kind: msgUser, Text: "first request", TurnIndex: 1},
		{Kind: msgAssistant, Text: "first response"},
	}
	m.session.scrollMode = true
	m.browseSelected = 0

	got := m.sessionView()
	if !strings.Contains(got, "▶ > first request") {
		t.Fatalf("selected user message not highlighted:\n%s", got)
	}
	if strings.Contains(got, "▶ first response") {
		t.Fatalf("unselected assistant message was highlighted:\n%s", got)
	}
}

func TestHubModelBrowsePageKeysMoveSelection(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 100
	m.session.width = 100
	m.session.viewport.Height = 3
	for i := 1; i <= 8; i++ {
		m.session.messages = append(m.session.messages, chatMessage{Kind: msgUser, Text: fmt.Sprintf("request %d", i), TurnIndex: i})
	}
	m.enterSessionBrowse(false)
	if m.browseSelected != 7 {
		t.Fatalf("initial browse selection=%d, want last message", m.browseSelected)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	got := updated.(hubModel)
	if got.browseSelected != 4 {
		t.Fatalf("pgup selection=%d, want 4", got.browseSelected)
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	got = updated.(hubModel)
	if got.browseSelected != 7 {
		t.Fatalf("pgdown selection=%d, want 7", got.browseSelected)
	}
}

func TestHubModelBrowseLineKeysMoveSelection(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []chatMessage{
		{Kind: msgUser, Text: "first", TurnIndex: 1},
		{Kind: msgAssistant, Text: "middle"},
		{Kind: msgUser, Text: "last", TurnIndex: 2},
	}
	m.enterSessionBrowse(false)
	if m.browseSelected != 2 {
		t.Fatalf("initial browse selection=%d, want last message", m.browseSelected)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	got := updated.(hubModel)
	if got.browseSelected != 1 {
		t.Fatalf("up selection=%d, want 1", got.browseSelected)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	got = updated.(hubModel)
	if got.browseSelected != 0 {
		t.Fatalf("k selection=%d, want 0", got.browseSelected)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyDown})
	got = updated.(hubModel)
	if got.browseSelected != 1 {
		t.Fatalf("down selection=%d, want 1", got.browseSelected)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	got = updated.(hubModel)
	if got.browseSelected != 2 {
		t.Fatalf("j selection=%d, want 2", got.browseSelected)
	}
}

func TestHubModelSessionBrowseExitKeysReturnToCompose(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.KeyMsg
	}{
		{name: "esc", msg: tea.KeyMsg{Type: tea.KeyEsc}},
		{name: "i", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}}},
		{name: "q", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newSessionHubModel(nil)
			m.session.messages = []chatMessage{{Kind: msgUser, Text: "request", TurnIndex: 1}}
			m.enterSessionBrowse(false)
			updated, _ := m.Update(tc.msg)
			got := updated.(hubModel)
			if got.session.scrollMode {
				t.Fatalf("%s should return to compose", tc.msg.String())
			}
			if got.browseSelected != -1 {
				t.Fatalf("%s browse selection=%d, want -1", tc.msg.String(), got.browseSelected)
			}
		})
	}
}

func TestHubModelCtrlOReturnsDashboardFromSession(t *testing.T) {
	m := newSessionHubModel(nil)
	m.rows = []hubRow{{kind: hubRowProject, project: "serf", projectKey: "serf"}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
}

func TestHubModelSlashDashboardAndProjectNavigate(t *testing.T) {
	m := newSessionHubModel(nil)
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.detail.Project = "serf"

	m.session.setInputValue("/dashboard")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/dashboard should not need an async command")
	}
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("/dashboard mode=%v", got.mode)
	}

	got.mode = hubModeSession
	got.session.setInputValue("/project")
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/project should not need an async command")
	}
	got = updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("/project mode=%v, want dashboard", got.mode)
	}
	rows := got.dashboardRows()
	if got.selected >= len(rows) || rows[got.selected].kind != hubRowProject || rows[got.selected].projectKey != "serf" {
		t.Fatalf("/project selected row=%d rows=%+v, want serf project row", got.selected, rows)
	}
}

func TestHubModelEnterOpensSessionDetail(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
			return threadListResponse(hubTreeResponse{Live: []hubTreeNode{{
				Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true,
			}}}), nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{
				Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true,
			}, "/tmp/serf")}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	updated, _ := m.Update(fetchHubTree(client)())
	updated, _ = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, cmd := updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not return a session fetch command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel).View()
	for _, want := range []string{"live task", "01LIVE", "/tmp/serf"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSessionHeaderShowsCodexMetadata(t *testing.T) {
	thread := appwire.Thread{
		ID:            "01CODEX",
		SessionID:     "01CODEX",
		Source:        "codex-local",
		Name:          "codex task",
		ModelProvider: "gpt-5.3-codex",
		CWD:           "/tmp/serf",
		Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Turns: []appwire.Turn{
			{ID: "turn_1", Status: appwire.TurnStatusCompleted},
			{ID: "turn_2", Status: appwire.TurnStatusInProgress},
		},
		Serf: appwire.SerfThread{
			Ref:             "codex-local:01CODEX",
			Profile:         "openai",
			ContextPressure: 0.73,
			Capabilities:    appwire.ThreadCapabilities{Send: true, Steer: true},
		},
	}
	m := newSessionHubModel(nil)
	m.width = 120
	m.detail = hubDetailFromThread(thread)

	got := m.sessionView()
	for _, want := range []string{
		"codex task",
		"source: codex-local",
		"state: active",
		"model: gpt-5.3-codex",
		"project: serf",
		"cwd: /tmp/serf",
		"turns: 2",
		"ctx: 73%",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("session header missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSessionHeaderShowsProviderWhenModelUnknown(t *testing.T) {
	thread := appwire.Thread{
		ID:        "01CODEX",
		SessionID: "01CODEX",
		Source:    "codex-local",
		Name:      "codex replay",
		CWD:       "/tmp/serf",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded},
		Serf: appwire.SerfThread{
			Ref:     "codex-local:01CODEX",
			Profile: "openai",
		},
	}
	m := newSessionHubModel(nil)
	m.width = 120
	m.detail = hubDetailFromThread(thread)

	got := m.sessionView()
	if !strings.Contains(got, "provider: openai") {
		t.Fatalf("session header missing provider metadata:\n%s", got)
	}
	if strings.Contains(got, "model: openai") || strings.Contains(got, "Model:    openai") {
		t.Fatalf("provider was mislabeled as model:\n%s", got)
	}
}

func TestHubModelSessionStatusLineUsesProfileWhenModelHasNoProviderPrefix(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 120
	m.detail = hubSessionDetail{
		Ref:         "codex-local:01CODEX",
		SourceLabel: "codex-local",
		Title:       "codex task",
		State:       "idle",
		Model:       "gpt-5.3-codex",
		Profile:     "openai",
	}

	got := m.sessionView()
	if !strings.Contains(got, "provider: openai") {
		t.Fatalf("status line missing provider profile:\n%s", got)
	}
	if strings.Contains(got, "auth: unknown") {
		t.Fatalf("status line ignored provider profile:\n%s", got)
	}
}

func TestHubModelSessionHeaderHandlesMissingMetadata(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail = hubSessionDetail{Ref: "local:01MISSING"}

	got := m.sessionView()
	for _, want := range []string{
		"local:01MISSING",
		"source: serf",
		"state: unknown",
		"model: unknown",
		"project: unknown",
		"cwd: unknown",
		"turns: 0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing-metadata session header missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSessionHeaderTruncatesLongLabels(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 64
	m.detail = hubSessionDetail{
		Ref:         "codex-local-with-long-name:01LONG",
		SourceLabel: "codex-local-with-long-name",
		Title:       "review the very long generated migration transcript without overlap",
		State:       "active",
		Model:       "openai/gpt-5.3-super-long-model-name-for-terminal-testing",
		WorkingDir:  "/Users/jesse/Documents/GitHub/prime-radiant-inc/serf/.worktrees/tui-257-session-composer",
		Project:     "serf",
		TurnCount:   42,
	}

	got := m.sessionView()
	lines := strings.Split(got, "\n")
	for i, line := range lines[:min(4, len(lines))] {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("header line %d width=%d exceeds %d:\n%s", i, lipgloss.Width(line), m.width, got)
		}
	}
}

func TestHubModelSessionStatusLineShowsConnectionAuthBusyAndError(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {})
	defer cleanup()

	m := newSessionHubModel(client)
	m.width = 120
	m.detail.Model = "openai/gpt-5"
	m.detail.State = "active"
	m.detail.ActiveTurnID = "turn_busy"
	updated, _ := m.Update(hubAuthStatusMsg{status: appwire.AuthStatusResponse{
		Provider:     "openai",
		Supported:    true,
		SignedIn:     true,
		ActiveSource: "oauth",
		Email:        "bot@example.com",
	}})
	m = updated.(hubModel)
	m.err = fmt.Errorf("recoverable send failed")

	got := m.sessionView()
	for _, want := range []string{
		"status: hub connected",
		"auth: openai oauth",
		"busy: turn_busy",
		"error: recoverable send failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status line missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSessionStatusLineReflectsCapabilityChanges(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 100

	if got := m.sessionView(); !strings.Contains(got, "send: ready") {
		t.Fatalf("send-ready status missing:\n%s", got)
	}

	m.detail.State = "active"
	m.detail.Capabilities.Send = true
	m.detail.Capabilities.Steer = true
	m.detail.Capabilities.Queue = true
	m.detail.ActiveTurnID = "turn_busy"
	if got := m.sessionView(); !strings.Contains(got, "queue: ready") {
		t.Fatalf("queue-ready status missing:\n%s", got)
	}

	m.detail.Capabilities.Queue = false
	if got := m.sessionView(); !strings.Contains(got, "send: ready") {
		t.Fatalf("send-ready active status missing:\n%s", got)
	}

	m.detail.Capabilities.Send = false
	if got := m.sessionView(); !strings.Contains(got, "read-only: source does not advertise queue") {
		t.Fatalf("read-only status missing:\n%s", got)
	}
}

func TestHubModelDashboardSpawnUsesSelectedProjectWorkingDir(t *testing.T) {
	var gotSpawn appwire.ThreadStartParams
	var gotModelList appwire.ModelListParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
			gotModelList = params
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
			gotSpawn = params
			return appwire.ThreadStartResponse{Thread: appwireThread(hubTreeNode{
				Ref: "local:02NEW", SessionID: "02NEW", Title: "new session", State: "idle", Project: "serf", Live: true,
			}, "/tmp/serf")}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{
				Ref: "local:02NEW", SessionID: "02NEW", Title: "new session", State: "idle", Project: "serf", Live: true,
			}, "/tmp/serf")}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key: "serf", Name: "serf", WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("dashboard spawn should fetch models")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	form := updated.(hubModel)
	form.session.setInputValue("build the thing")
	updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("spawn form submit returned nil command")
	}
	updated, cmd = updated.(hubModel).Update(cmd())
	if cmd == nil {
		t.Fatal("spawn response did not fetch new session detail")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)

	if gotSpawn.CWD != "/tmp/serf" {
		t.Fatalf("cwd=%q, want /tmp/serf", gotSpawn.CWD)
	}
	if gotModelList.CWD != "/tmp/serf" {
		t.Fatalf("model list cwd=%q, want /tmp/serf", gotModelList.CWD)
	}
	if testInputText(gotSpawn.Input) != "build the thing" {
		t.Fatalf("prompt=%q, want build the thing", testInputText(gotSpawn.Input))
	}
	if gotSpawn.ModelProvider != "" || gotSpawn.Model != "openai/gpt-5" {
		t.Fatalf("model=%s/%s, want openai/gpt-5", gotSpawn.ModelProvider, gotSpawn.Model)
	}
	if got.mode != hubModeSession || got.detail.SessionID != "02NEW" {
		t.Fatalf("mode=%v detail=%+v", got.mode, got.detail)
	}
}

func TestHubSpawnSendsHarnessSeparatelyFromModel(t *testing.T) {
	var gotSpawn appwire.ThreadStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
			gotSpawn = params
			return appwire.ThreadStartResponse{Thread: appwireThread(hubTreeNode{
				Ref: "codex:02NEW", SessionID: "02NEW", Title: "new session", State: "idle", Project: "serf", Live: true,
			}, "/tmp/serf")}, nil
		})
	})
	defer cleanup()

	cmd := sendHubSpawn(client, hubSpawnRequest{
		Prompt:     "build the thing",
		Harness:    "codex",
		Model:      "openai/gpt-5",
		WorkingDir: "/tmp/serf",
	})
	if cmd == nil {
		t.Fatal("spawn command was nil")
	}
	msg := cmd().(hubSpawnMsg)
	if msg.err != nil {
		t.Fatalf("spawn: %v", msg.err)
	}
	if gotSpawn.Harness != "codex" || gotSpawn.ModelProvider != "" || gotSpawn.Model != "openai/gpt-5" {
		t.Fatalf("spawn params=%+v", gotSpawn)
	}
}

func TestHubModelSpawnCyclesConfiguredHarnesses(t *testing.T) {
	var gotSpawn appwire.ThreadStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
			return appwire.HarnessListResponse{Data: []appwire.HarnessDescriptor{
				{ID: "serf", Label: "serf", Kind: "serf"},
				{ID: "codex-local", Label: "codex-local", Kind: "codex"},
			}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
			gotSpawn = params
			return appwire.ThreadStartResponse{Thread: appwireThread(hubTreeNode{
				Ref: "codex-local:02NEW", SessionID: "02NEW", Title: "new session", State: "idle", Project: "serf", Live: true,
			}, "/tmp/serf")}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key: "serf", Name: "serf", WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true}},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("dashboard spawn should fetch spawn options")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	form := updated.(hubModel)
	updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyTab})
	form = updated.(hubModel)
	updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	form = updated.(hubModel)
	if form.spawnModel != "" {
		t.Fatalf("codex harness carried stale model %q", form.spawnModel)
	}
	view := form.spawnView()
	if strings.Contains(view, "openai/gpt-5") {
		t.Fatalf("codex harness offered serf model:\n%s", view)
	}
	updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	form = updated.(hubModel)
	updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("spawn form submit returned nil command")
	}
	msg := cmd().(hubSpawnMsg)
	if msg.err != nil {
		t.Fatalf("spawn: %v", msg.err)
	}
	if gotSpawn.Harness != "codex-local" || gotSpawn.ModelProvider != "" || gotSpawn.Model != "" {
		t.Fatalf("spawn params=%+v", gotSpawn)
	}
}

func TestHubModelCodexSpawnSurvivesModelListFailure(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{}, appwire.Unavailable("models unavailable")
		})
		appserver.HandleTyped(app.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
			return appwire.HarnessListResponse{Data: []appwire.HarnessDescriptor{
				{ID: "codex-local", Label: "codex-local", Kind: "codex"},
			}}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key: "serf", Name: "serf", WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true}},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("dashboard spawn should fetch spawn options")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	form := updated.(hubModel)
	if form.err != nil {
		t.Fatalf("codex spawn surfaced serf model-list error: %v", form.err)
	}
	if form.spawnHarness != "codex-local" || form.spawnHarnessKind() != "codex" {
		t.Fatalf("spawn harness=%q kind=%q", form.spawnHarness, form.spawnHarnessKind())
	}
	if form.spawnModel != "" {
		t.Fatalf("codex spawn retained model %q", form.spawnModel)
	}
	if view := form.spawnView(); !strings.Contains(view, "harness default") {
		t.Fatalf("codex spawn should use harness default model:\n%s", view)
	}
}

func TestHubModelCodexSpawnOpensHarnessModelPicker(t *testing.T) {
	var gotParams appwire.ModelListParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
			gotParams = params
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "codex-local", Model: "gpt-5.3-codex"}}}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.openSpawnForm()
	m.spawnHarnesses = []string{"serf", "codex-local"}
	m.spawnHarnessKinds = map[string]string{"serf": "serf", "codex-local": "codex"}
	m.spawnHarness = "codex-local"
	m.spawnDir = "/tmp/serf"
	m.spawnModels = []modelPickerItem{{id: "openai/gpt-5", display: "openai/gpt-5"}}
	m.spawnModel = ""

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(hubModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(hubModel)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("codex model field should fetch harness models")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)
	if gotParams.Harness != "codex-local" {
		t.Fatalf("model list params=%+v, want codex harness", gotParams)
	}
	if gotParams.CWD != "/tmp/serf" {
		t.Fatalf("model list cwd=%q, want /tmp/serf", gotParams.CWD)
	}
	if got.spawnModelPicker == nil {
		t.Fatalf("codex harness did not open model picker:\n%s", got.spawnView())
	}
	if view := got.spawnModelPicker.View(); !strings.Contains(view, "codex-local/gpt-5.3-codex") || strings.Contains(view, "openai/gpt-5") {
		t.Fatalf("codex harness picker should show only codex models:\n%s", view)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(hubModel)
	if got.spawnModel != "gpt-5.3-codex" {
		t.Fatalf("codex harness selected model=%q, want raw codex model id", got.spawnModel)
	}
	if view := got.spawnView(); !strings.Contains(view, "codex-local/gpt-5.3-codex") {
		t.Fatalf("codex spawn view should show harness/model relationship:\n%s", view)
	}
}

func TestHubModelSpawnRejectsHubUnsupportedEmptyTaskBeforeStart(t *testing.T) {
	var startCalled bool
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		app.Router().Handle(appwire.MethodSerfHarnessesList, func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"data": []map[string]any{{
				"id":                             "serf",
				"label":                          "serf",
				"kind":                           "serf",
				"emptyTaskUnsupportedReason":     "task text is required by this hub",
				"emptyTaskUnsupportedNextAction": "enter a task before spawning",
			}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
			startCalled = true
			return appwire.ThreadStartResponse{}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "serf",
		Name:       "serf",
		WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("spawn key should fetch options")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	form := updated.(hubModel)
	updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("empty unsupported task should fail before thread/start")
	}
	form = updated.(hubModel)
	if startCalled {
		t.Fatal("thread/start was called for a Hub-unsupported empty task")
	}
	if form.mode != hubModeSpawn || form.session.input.Value() != "" {
		t.Fatalf("spawn form should stay open with draft preserved: mode=%v draft=%q", form.mode, form.session.input.Value())
	}
	if view := form.spawnView(); !strings.Contains(view, "Spawn unavailable") || !strings.Contains(view, "task text is required") || !strings.Contains(view, "enter a task before spawning") {
		t.Fatalf("spawn rejection was not structured:\n%s", view)
	}
}

func TestHubModelSpawnDefaultsToEnabledModelWhenAuthRequired(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{
					{Provider: "openai", Model: "gpt-5"},
					{Provider: "ollama", Model: "llama3"},
				},
				Diagnostics: []appwire.ModelListDiagnostic{{
					Provider: "openai",
					Title:    "Login required",
					Message:  "OpenAI login required",
					Hint:     "run /auth openai",
				}},
			}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "serf",
		Name:       "serf",
		WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("spawn key should fetch options")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	form := updated.(hubModel)
	if form.spawnModel != "ollama/llama3" {
		t.Fatalf("spawn model=%q, want first enabled ollama/llama3", form.spawnModel)
	}
	updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyTab})
	form = updated.(hubModel)
	updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyTab})
	form = updated.(hubModel)
	updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	form = updated.(hubModel)
	if form.spawnModelPicker == nil {
		t.Fatalf("model field did not open picker:\n%s", form.spawnView())
	}
	if view := form.spawnModelPicker.View(); !strings.Contains(view, "openai/gpt-5") || !strings.Contains(view, "disabled: Login required") || !strings.Contains(view, "ollama/llama3") {
		t.Fatalf("spawn picker missing disabled auth-required row:\n%s", view)
	}
}

func TestHubModelDashboardSpawnOpensFormBeforePosting(t *testing.T) {
	var posted bool
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
			posted = true
			return appwire.ThreadStartResponse{}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "serf",
		Name:       "serf",
		WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("spawn key should fetch models")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)
	if posted {
		t.Fatal("spawn key posted before form submission")
	}
	if !strings.Contains(got.View(), "serf / new session") || !strings.Contains(got.View(), "/tmp/serf") || !strings.Contains(got.View(), "openai/gpt-5") {
		t.Fatalf("spawn form not rendered:\n%s", got.View())
	}
}

func TestHubModelDashboardSpawnEditsWorkingDirBeforePosting(t *testing.T) {
	var gotSpawn appwire.ThreadStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
			gotSpawn = params
			return appwire.ThreadStartResponse{Thread: appwireThread(hubTreeNode{
				Ref: "local:02NEW", SessionID: "02NEW", Title: "new session", State: "idle", Project: "custom", Live: true,
			}, "/tmp/custom-serf")}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "serf",
		Name:       "serf",
		WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("spawn key should fetch models")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	form := updated.(hubModel)
	for range 3 {
		updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyTab})
		if cmd != nil {
			t.Fatal("tab to directory returned unexpected command")
		}
		form = updated.(hubModel)
	}
	if form.spawnFocus != hubSpawnFieldDir {
		t.Fatalf("spawn focus=%v, want directory", form.spawnFocus)
	}
	if view := form.spawnView(); !strings.Contains(view, "> Dir:") {
		t.Fatalf("directory field was not focused:\n%s", view)
	}
	updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	form = updated.(hubModel)
	for _, r := range "/tmp/custom-serf" {
		updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		form = updated.(hubModel)
	}
	updated, _ = form.Update(tea.KeyMsg{Type: tea.KeyTab})
	form = updated.(hubModel)
	form.session.setInputValue("spawn with custom cwd")

	updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("spawn form submit returned nil command")
	}
	msg := cmd().(hubSpawnMsg)
	if msg.err != nil {
		t.Fatalf("spawn: %v", msg.err)
	}
	if gotSpawn.CWD != "/tmp/custom-serf" {
		t.Fatalf("spawn cwd=%q, want /tmp/custom-serf", gotSpawn.CWD)
	}
	if testInputText(gotSpawn.Input) != "spawn with custom cwd" {
		t.Fatalf("spawn prompt=%q", testInputText(gotSpawn.Input))
	}
}

func TestHubModelSpawnPromptAcceptsHarnessAndModelLetters(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()

	for _, r := range []rune{'m', 'h'} {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(hubModel)
	}

	if got := m.session.input.Value(); got != "mh" {
		t.Fatalf("spawn prompt=%q, want mh", got)
	}
}

func TestHubModelSpawnFormFocusControlsHarnessAndModel(t *testing.T) {
	var gotParams appwire.ModelListParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
			gotParams = params
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "codex-local", Model: "gpt-5.3-codex"}}}, nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.openSpawnForm()
	m.spawnDir = "/tmp/serf"
	m.spawnHarnesses = []string{"serf", "codex-local"}
	m.spawnHarnessKinds = map[string]string{"serf": "serf", "codex-local": "codex"}
	m.spawnModels = []modelPickerItem{{id: "openai/gpt-5", display: "openai/gpt-5"}}
	m.spawnModel = "openai/gpt-5"

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatal("tab to harness returned unexpected command")
	}
	form := updated.(hubModel)
	updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("harness field change returned unexpected command")
	}
	form = updated.(hubModel)
	if form.spawnHarness != "codex-local" || form.spawnModel != "" {
		t.Fatalf("harness=%q model=%q, want codex-local with cleared model", form.spawnHarness, form.spawnModel)
	}

	updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatal("tab to model returned unexpected command")
	}
	form = updated.(hubModel)
	updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("model field should fetch codex harness models")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	form = updated.(hubModel)
	if gotParams.Harness != "codex-local" || gotParams.CWD != "/tmp/serf" {
		t.Fatalf("model list params=%+v, want codex harness in /tmp/serf", gotParams)
	}
	if form.spawnModelPicker == nil {
		t.Fatalf("model field did not open model picker:\n%s", form.spawnView())
	}
}

func TestHubDashboardSpawnWaitsForSlowHubSpawn(t *testing.T) {
	var gotSpawn appwire.ThreadStartParams
	app := appserver.NewServer(appserver.ServerConfig{
		ServerName: "serf-hub",
		SourceID:   "local",
		Features:   appwire.FeatureSet{},
	})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return threadListResponse(hubTreeResponse{Projects: []hubTreeProject{{
			Key:        "serf",
			Name:       "serf",
			WorkingDir: "/tmp/serf",
			Sessions: []hubTreeNode{
				{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
			},
		}}}), nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: []appwire.HarnessDescriptor{{ID: "serf", Label: "serf", Kind: "serf"}}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		gotSpawn = params
		time.Sleep(1500 * time.Millisecond)
		return appwire.ThreadStartResponse{Thread: appwireThread(hubTreeNode{
			Ref: "local:02SLOW", SessionID: "02SLOW", Title: "spawned session", State: "idle", Project: "serf", Live: true,
		}, "/tmp/serf")}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{
			Ref: "local:02SLOW", SessionID: "02SLOW", Title: "spawned session", State: "idle", Project: "serf", Live: true,
		}, "/tmp/serf")}, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		app.ServeWebSocket(w, r)
	}))
	defer srv.Close()

	runtime, err := startHubClient(context.Background(), hubStartConfig{
		RawAddr:    srv.URL,
		AutoStart:  false,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("start hub client: %v", err)
	}
	defer runtime.Client.Close()

	model := newHubModel(runtime.Client, runtime.Address.BaseURL)
	updated, cmd := model.Update(fetchHubTree(runtime.Client)())
	if cmd != nil {
		t.Fatal("initial tree load returned unexpected command")
	}
	model = updated.(hubModel)

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("dashboard spawn should fetch models")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	model = updated.(hubModel)
	model.session.setInputValue("slow spawn")

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("spawn form submit returned nil command")
	}
	updated, cmd = updated.(hubModel).Update(cmd())
	model = updated.(hubModel)
	if model.err != nil {
		t.Fatalf("spawn failed: %v", model.err)
	}
	if cmd == nil {
		t.Fatal("spawn response did not fetch new session detail")
	}

	updated, cmd = model.Update(cmd())
	if cmd != nil {
		t.Fatal("session detail returned unexpected command")
	}
	model = updated.(hubModel)
	if model.mode != hubModeSession || model.detail.SessionID != "02SLOW" {
		t.Fatalf("mode=%v detail=%+v", model.mode, model.detail)
	}
	if gotSpawn.CWD != "/tmp/serf" {
		t.Fatalf("cwd=%q, want /tmp/serf", gotSpawn.CWD)
	}
	if testInputText(gotSpawn.Input) != "slow spawn" {
		t.Fatalf("prompt=%q, want slow spawn", testInputText(gotSpawn.Input))
	}
	if gotSpawn.ModelProvider != "" || gotSpawn.Model != "openai/gpt-5" {
		t.Fatalf("model=%s/%s, want openai/gpt-5", gotSpawn.ModelProvider, gotSpawn.Model)
	}
}

func TestHubModelNotificationClosedIsNotError(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	updated, _ := m.Update(hubNotificationMsg{ok: false})
	got := updated.(hubModel)
	if got.err != nil {
		t.Fatalf("err=%v", got.err)
	}
}

func TestHubModelStatusIdleRefreshesSessionCapabilities(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			if params.Ref != "local:01SEND" {
				t.Fatalf("ref=%q, want local:01SEND", params.Ref)
			}
			return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{
				Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", State: "idle", Model: "gpt-5", Project: "serf", Live: true,
			}, "/tmp/serf")}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.State = appwire.ThreadStatusActive
	m.detail.Capabilities.Send = false
	m.session.processing = true
	notification := appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
		Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
	})

	cmd := m.applyHubNotification(*notification.Notification)
	if cmd == nil {
		t.Fatal("idle status notification should refresh session detail")
	}
	updated, _ := m.Update(cmd())
	got := updated.(hubModel)
	if got.sessionPanel != nil {
		t.Fatalf("idle status refresh should not open details panel: %+v", got.sessionPanel)
	}
	if !got.detail.Capabilities.Send {
		t.Fatalf("send capability was not refreshed: %+v", got.detail.Capabilities)
	}
	if got.session.processing {
		t.Fatal("session stayed processing after idle status refresh")
	}
	if view := got.sessionView(); !strings.Contains(view, "send: ready") {
		t.Fatalf("session view did not show send-ready after refresh:\n%s", view)
	}
}

// kata 4yvd: idle→processing must refresh capabilities so /interrupt becomes
// available mid-turn without the user having to run /status first.
func TestHubModelStatusProcessingRefreshesSessionCapabilities(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			if params.Ref != "local:01SEND" {
				t.Fatalf("ref=%q, want local:01SEND", params.Ref)
			}
			thread := appwireThread(hubTreeNode{
				Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", State: "active", Model: "gpt-5", Project: "serf", Live: true,
			}, "/tmp/serf")
			// Source's mid-turn capability snapshot: send/steer flip with state,
			// but interrupt is freshly advertised.
			thread.Serf.Capabilities.Send = false
			thread.Serf.Capabilities.Steer = true
			thread.Serf.Capabilities.Interrupt = true
			return appwire.ThreadReadResponse{Thread: thread}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	// Stale idle snapshot: interrupt/steer disabled, as the source advertised them
	// before the turn started.
	m.detail.State = appwire.ThreadStatusIdle
	m.detail.Capabilities.Interrupt = false
	m.detail.Capabilities.Steer = false
	m.session.processing = false
	notification := appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
		Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
	})

	cmd := m.applyHubNotification(*notification.Notification)
	if cmd == nil {
		t.Fatal("processing status notification should refresh session detail")
	}
	if !m.session.processing {
		t.Fatal("session.processing should flip to true immediately on status change")
	}
	updated, _ := m.Update(cmd())
	got := updated.(hubModel)
	if !got.detail.Capabilities.Interrupt {
		t.Fatalf("interrupt capability was not refreshed: %+v", got.detail.Capabilities)
	}
	if !got.detail.Capabilities.Steer {
		t.Fatalf("steer capability was not refreshed: %+v", got.detail.Capabilities)
	}
	if got.detail.Capabilities.Send {
		t.Fatalf("send should reflect the source's mid-turn snapshot (false): %+v", got.detail.Capabilities)
	}
}

func TestHubModelStatusRefreshIgnoresStaleSessionRead(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = appwire.ThreadStatusIdle
	m.detail.Capabilities.Send = true
	m.detail.Capabilities.Interrupt = false
	m.session.processing = false

	updated, _ := m.Update(hubSessionMsg{
		expectedState: appwire.ThreadStatusActive,
		detail: hubSessionDetail{
			Ref:   "local:01SEND",
			State: appwire.ThreadStatusActive,
			Capabilities: hubSessionCapabilities{
				Send:      false,
				Interrupt: true,
			},
		},
	})
	got := updated.(hubModel)
	if got.detail.State != appwire.ThreadStatusIdle {
		t.Fatalf("state=%q, want stale processing refresh ignored", got.detail.State)
	}
	if !got.detail.Capabilities.Send || got.detail.Capabilities.Interrupt {
		t.Fatalf("capabilities=%+v, want stale refresh ignored", got.detail.Capabilities)
	}

	got.detail.State = appwire.ThreadStatusActive
	got.detail.Capabilities.Send = false
	got.detail.Capabilities.Interrupt = true
	got.statusRefreshToken = 2
	updated, _ = got.Update(hubSessionMsg{
		ref:                  "local:01SEND",
		expectedState:        appwire.ThreadStatusActive,
		expectedRefreshToken: 1,
		err:                  errors.New("old refresh failed"),
	})
	got = updated.(hubModel)
	if got.err != nil {
		t.Fatalf("err=%v, want stale refresh error ignored", got.err)
	}

	updated, _ = got.Update(hubSessionMsg{
		expectedState:        appwire.ThreadStatusActive,
		expectedRefreshToken: 1,
		detail: hubSessionDetail{
			Ref:   "local:01SEND",
			State: appwire.ThreadStatusActive,
			Capabilities: hubSessionCapabilities{
				Send: true,
			},
		},
	})
	got = updated.(hubModel)
	if got.detail.State != appwire.ThreadStatusActive || got.detail.Capabilities.Send || !got.detail.Capabilities.Interrupt {
		t.Fatalf("detail=%+v, want old matching-state refresh token ignored", got.detail)
	}

	got.detail.Ref = "local:02OTHER"
	got.detail.SessionID = "02OTHER"
	got.session.sessionID = "02OTHER"
	got.statusRefreshToken = 2
	updated, _ = got.Update(hubSessionMsg{
		ref:                  "local:01SEND",
		expectedState:        appwire.ThreadStatusActive,
		expectedRefreshToken: 2,
		detail: hubSessionDetail{
			Ref:       "local:01SEND",
			SessionID: "01SEND",
			State:     appwire.ThreadStatusActive,
			Capabilities: hubSessionCapabilities{
				Send: true,
			},
		},
	})
	got = updated.(hubModel)
	if got.detail.Ref != "local:02OTHER" || got.session.sessionID != "02OTHER" {
		t.Fatalf("stale refresh reactivated old session: detail=%+v sessionID=%q", got.detail, got.session.sessionID)
	}

	got.mode = hubModeDashboard
	got.detail.Ref = "local:01SEND"
	got.detail.SessionID = "01SEND"
	got.session.sessionID = "01SEND"
	got.statusRefreshToken = 2
	updated, _ = got.Update(hubSessionMsg{
		ref:                  "local:01SEND",
		expectedState:        appwire.ThreadStatusActive,
		expectedRefreshToken: 2,
		detail: hubSessionDetail{
			Ref:       "local:01SEND",
			SessionID: "01SEND",
			State:     appwire.ThreadStatusActive,
		},
	})
	got = updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("refresh reactivated session after leaving session mode: mode=%v", got.mode)
	}

	updated, _ = got.Update(hubSessionMsg{
		ref:                  "local:01SEND",
		expectedState:        appwire.ThreadStatusActive,
		expectedRefreshToken: 2,
		err:                  errors.New("late refresh failed"),
	})
	got = updated.(hubModel)
	if got.err != nil {
		t.Fatalf("stale refresh error leaked outside session mode: %v", got.err)
	}

	got.mode = hubModeSession
	got.detail.Ref = "local:01SEND"
	got.detail.SessionID = "01SEND"
	got.session.sessionID = "01SEND"
	updated, _ = got.Update(hubSessionMsg{
		expectedState:        appwire.ThreadStatusActive,
		expectedRefreshToken: 2,
		detail: hubSessionDetail{
			Ref:   "local:01SEND",
			State: appwire.ThreadStatusIdle,
			Capabilities: hubSessionCapabilities{
				Send: true,
			},
		},
	})
	got = updated.(hubModel)
	if got.detail.State != appwire.ThreadStatusActive {
		t.Fatalf("state=%q, want stale idle response ignored", got.detail.State)
	}
	if got.detail.Capabilities.Send || !got.detail.Capabilities.Interrupt {
		t.Fatalf("capabilities=%+v, want stale idle response ignored", got.detail.Capabilities)
	}

	updated, _ = got.Update(hubSessionMsg{
		expectedState:        appwire.ThreadStatusActive,
		expectedRefreshToken: 2,
		detail: hubSessionDetail{
			Ref:   "local:01SEND",
			State: appwire.ThreadStatusActive,
			Capabilities: hubSessionCapabilities{
				Interrupt: true,
			},
		},
	})
	got = updated.(hubModel)
	if got.detail.State != appwire.ThreadStatusActive || !got.detail.Capabilities.Interrupt {
		t.Fatalf("detail=%+v, want matching processing refresh applied", got.detail)
	}
}

func TestStatusRefreshStatesMatchExpectedRequiresCurrentAndPayload(t *testing.T) {
	if statusRefreshStatesMatchExpected(appwire.ThreadStatusActive, appwire.ThreadStatusIdle, appwire.ThreadStatusActive) {
		t.Fatal("expected stale payload state to fail")
	}
	if statusRefreshStatesMatchExpected(appwire.ThreadStatusIdle, appwire.ThreadStatusActive, appwire.ThreadStatusActive) {
		t.Fatal("expected stale current state to fail")
	}
	if !statusRefreshStatesMatchExpected(appwire.ThreadStatusActive, appwire.ThreadStatusActive, appwire.ThreadStatusActive) {
		t.Fatal("expected matching current and payload states to pass")
	}
}

func TestHubModelSendUsesAppWireTurnStart(t *testing.T) {
	var got appwire.TurnStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			got = params
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("ship it")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("send returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	gotModel := updated.(hubModel)
	if got.Ref != "local:01SEND" || testInputText(got.Input) != "ship it" {
		t.Fatalf("params=%+v", got)
	}
	if gotModel.session.input.Value() != "" {
		t.Fatalf("input not reset: %q", gotModel.session.input.Value())
	}
}

func TestHubModelBusySendPreservesInput(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{}, fmt.Errorf("busy")
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("keep me")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("send returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)
	if got.session.input.Value() != "keep me" {
		t.Fatalf("input=%q", got.session.input.Value())
	}
}

func TestHubModelTasksAndDetailsUseAppWire(t *testing.T) {
	var methods []string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodSerfTasksList, func(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
			methods = append(methods, appwire.MethodSerfTasksList)
			return appwire.TaskListResponse{Data: []agent.Task{{ID: 1, Type: agent.TaskTypeImplement, Description: "wire actions", Status: agent.TaskDone}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			methods = append(methods, appwire.MethodThreadRead)
			return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{
				Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", State: "idle", Project: "details", Live: true,
			}, "/tmp/details")}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("/tasks")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(hubModel).Update(cmd())
	if !strings.Contains(updated.(hubModel).View(), "wire actions") {
		t.Fatalf("tasks view missing:\n%s", updated.(hubModel).View())
	}

	got := updated.(hubModel)
	got.session.setInputValue("/details")
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(hubModel).Update(cmd())
	got = updated.(hubModel)
	if got.sessionPanel == nil || !strings.Contains(got.sessionPanel.Body, "/tmp/details") {
		t.Fatalf("details panel missing: %+v", got.sessionPanel)
	}
	if !strings.Contains(got.View(), "/tmp/details") {
		t.Fatalf("details view missing:\n%s", got.View())
	}
	if strings.Join(methods, ",") != appwire.MethodSerfTasksList+","+appwire.MethodThreadRead {
		t.Fatalf("methods=%v", methods)
	}
}

func TestHubModelSameSessionRefreshReplacesTranscript(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []chatMessage{{Kind: msgAssistant, Text: "stale transcript"}}
	m.session.activeTools = map[string]int{"call_old": 0}
	m.session.activeMessages = map[string]int{"item_old": 0}
	m.browseSelected = 0

	updated, _ := m.Update(hubSessionMsg{
		ref: "local:01SEND",
		detail: hubSessionDetail{
			Ref:       "local:01SEND",
			SessionID: "01SEND",
			State:     appwire.ThreadStatusIdle,
		},
		messages: []chatMessage{{Kind: msgAssistant, Text: "fresh transcript"}},
	})
	got := updated.(hubModel)
	if len(got.session.messages) != 1 || got.session.messages[0].Text != "fresh transcript" {
		t.Fatalf("messages=%+v, want fresh transcript", got.session.messages)
	}
	if len(got.session.activeTools) != 0 || len(got.session.activeMessages) != 0 {
		t.Fatalf("active transcript state not reset: tools=%+v messages=%+v", got.session.activeTools, got.session.activeMessages)
	}
	if got.browseSelected != -1 {
		t.Fatalf("browseSelected=%d, want reset", got.browseSelected)
	}
}

func TestHubModelExpectedStatusRefreshDoesNotReplaceTranscript(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = appwire.ThreadStatusActive
	m.session.messages = []chatMessage{{Kind: msgAssistant, Text: "live transcript"}}

	updated, _ := m.Update(hubSessionMsg{
		ref:           "local:01SEND",
		expectedState: appwire.ThreadStatusActive,
		detail: hubSessionDetail{
			Ref:       "local:01SEND",
			SessionID: "01SEND",
			State:     appwire.ThreadStatusActive,
		},
		messages: []chatMessage{{Kind: msgAssistant, Text: "status payload transcript"}},
	})
	got := updated.(hubModel)
	if len(got.session.messages) != 1 || got.session.messages[0].Text != "live transcript" {
		t.Fatalf("messages=%+v, want status refresh to preserve transcript", got.session.messages)
	}
}

func TestFetchHubSessionSubscribesToLiveThread(t *testing.T) {
	var got appwire.ThreadReadParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			got = params
			return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{
				Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", State: "idle", Project: "details", Live: true,
			}, "/tmp/details")}, nil
		})
	})
	defer cleanup()

	msg := fetchHubSession(client, appwire.Ref{SourceID: "local", ThreadID: "01SEND"})()
	if gotMsg, ok := msg.(hubSessionMsg); !ok || gotMsg.err != nil {
		t.Fatalf("msg=%T %+v", msg, msg)
	}
	if !got.Subscribe {
		t.Fatalf("ThreadRead Subscribe=false, want true")
	}
	if !got.ReplaceSubscription {
		t.Fatalf("ThreadRead ReplaceSubscription=false, want true")
	}
}

func TestFetchHubSessionStatusRefreshDoesNotReplaceLiveSubscription(t *testing.T) {
	var got appwire.ThreadReadParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			got = params
			return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{
				Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", State: "idle", Project: "details", Live: true,
			}, "/tmp/details")}, nil
		})
	})
	defer cleanup()

	msg := fetchHubSessionExpectingStateToken(client, appwire.Ref{SourceID: "local", ThreadID: "01SEND"}, "idle", 1)()
	if gotMsg, ok := msg.(hubSessionMsg); !ok || gotMsg.err != nil {
		t.Fatalf("msg=%T %+v", msg, msg)
	}
	if got.Subscribe {
		t.Fatalf("ThreadRead Subscribe=true, want false")
	}
	if got.ReplaceSubscription {
		t.Fatalf("ThreadRead ReplaceSubscription=true, want false")
	}
}

func TestHubModelStatusUsesHubThreadTasksAndAuth(t *testing.T) {
	var methods []string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			methods = append(methods, appwire.MethodThreadRead)
			thread := appwireThread(hubTreeNode{
				Ref: "local:01SEND", SessionID: "01SEND", Title: "send task", State: "idle", Model: "gpt-5", Project: "details", Live: true,
			}, "/tmp/details")
			thread.Serf.Profile = "openai"
			thread.Serf.ContextPressure = 0.42
			thread.Turns = []appwire.Turn{
				{ID: "turn_1", Status: appwire.TurnStatusCompleted},
				{ID: "turn_2", Status: appwire.TurnStatusFailed, Error: &appwire.TurnError{Message: "provider quota exceeded"}},
			}
			return appwire.ThreadReadResponse{Thread: thread}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodSerfTasksList, func(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
			methods = append(methods, appwire.MethodSerfTasksList)
			return appwire.TaskListResponse{Data: []agent.Task{
				{ID: 1, Type: agent.TaskTypeImplement, Description: "wire status", Status: agent.TaskDone},
				{ID: 2, Type: agent.TaskTypeVerify, Description: "verify status", Status: agent.TaskInProgress},
			}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodSerfAuthStatus, func(context.Context, appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
			methods = append(methods, appwire.MethodSerfAuthStatus)
			return appwire.AuthStatusResponse{Provider: "openai", Supported: true, SignedIn: true, ActiveSource: "oauth", Email: "jesse@example.test"}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("/status")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/status should fetch Hub status data")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	model := updated.(hubModel)
	if model.sessionPanel == nil {
		t.Fatal("/status should open a session panel")
	}
	if len(model.session.messages) != 0 {
		t.Fatalf("/status should not append diagnostics to transcript history: %+v", model.session.messages)
	}
	got := model.View()
	for _, want := range []string{
		"status",
		"Model:    gpt-5 (openai)",
		"Dir:      /tmp/details",
		"Turns:    2",
		"Context:  42% used",
		"Tasks:    1/2 done, 1 active",
		"Auth:     openai oauth jesse@example.test",
		"Recent errors:",
		"turn_2: provider quota exceeded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}
	if strings.Join(methods, ",") != appwire.MethodThreadRead+","+appwire.MethodSerfTasksList+","+appwire.MethodSerfAuthStatus {
		t.Fatalf("methods=%v", methods)
	}
}

func TestHubModelSessionPanelRendersAsCenteredModalOverlay(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []chatMessage{{Kind: msgCommunicate, Text: "main transcript answer"}}
	panel := hubSessionPanel{Body: "details\nSession:  01SEND\nDir:      /tmp/project"}
	m.sessionPanel = &panel

	m.width = 140
	got := m.sessionView()
	for _, want := range []string{"main transcript answer", "details", "Dir:      /tmp/project", "╭", "╰"} {
		if !strings.Contains(got, want) {
			t.Fatalf("details modal missing %q:\n%s", want, got)
		}
	}
	requireOrderedText(t, got, "details", "main transcript answer", "message")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("closing session panel should be synchronous")
	}
	if updated.(hubModel).sessionPanel != nil {
		t.Fatalf("escape should close session panel")
	}
}

func TestHubModelSessionPanelBoundsLongTranscriptToVisibleShell(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 140
	m.height = 18
	for i := 0; i < 30; i++ {
		m.session.messages = append(m.session.messages, chatMessage{Kind: msgCommunicate, Text: fmt.Sprintf("main transcript answer %02d", i)})
	}
	m.sessionPanel = &hubSessionPanel{Body: "details\nSession:  01SEND\nDir:      /tmp/project"}

	got := m.sessionView()
	if gotLines := renderedLineCount(got); gotLines > m.height {
		t.Fatalf("session view should fit terminal height; got %d lines for height %d:\n%s", gotLines, m.height, got)
	}
	if !strings.Contains(got, "Dir:      /tmp/project") {
		t.Fatalf("details panel should remain visible with long transcript:\n%s", got)
	}
}

func TestHubModelActionsAndClearUseAppWire(t *testing.T) {
	var methods []string
	var interruptTurnID string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, func(_ context.Context, params appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
			methods = append(methods, appwire.MethodTurnInterrupt)
			interruptTurnID = params.ExpectedTurnID
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadCompactStart, func(context.Context, appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
			methods = append(methods, appwire.MethodThreadCompactStart)
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, func(context.Context, appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
			methods = append(methods, appwire.MethodThreadModelSet)
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadShutdown, func(context.Context, appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
			methods = append(methods, appwire.MethodThreadShutdown)
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, func(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
			methods = append(methods, appwire.MethodThreadClear)
			thread := appwireThread(hubTreeNode{Ref: "local:02NEW", SessionID: "02NEW", Title: "new session", State: "idle", Project: "serf", Live: true}, "/tmp/serf")
			return appwire.ThreadClearResponse{Thread: thread, Ref: thread.Serf.Ref}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			methods = append(methods, appwire.MethodThreadRead)
			return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{Ref: "local:02NEW", SessionID: "02NEW", Title: "new session", State: "idle", Project: "serf", Live: true}, "/tmp/serf")}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.Capabilities.Shutdown = true
	for _, input := range []string{"/interrupt", "/compact", "/model gpt-5.5", "/shutdown"} {
		m.session.setInputValue(input)
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("%s returned nil command", input)
		}
		updated, _ = updated.(hubModel).Update(cmd())
		m = updated.(hubModel)
	}
	m.session.setInputValue("/clear")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, cmd = updated.(hubModel).Update(cmd())
	if cmd == nil {
		t.Fatal("clear did not return detail fetch command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	if updated.(hubModel).detail.SessionID != "02NEW" {
		t.Fatalf("detail=%+v", updated.(hubModel).detail)
	}
	want := strings.Join([]string{
		appwire.MethodTurnInterrupt,
		appwire.MethodThreadCompactStart,
		appwire.MethodThreadModelSet,
		appwire.MethodThreadShutdown,
		appwire.MethodThreadClear,
		appwire.MethodThreadRead,
	}, ",")
	if strings.Join(methods, ",") != want {
		t.Fatalf("methods=%v", methods)
	}
	if interruptTurnID != "turn_active" {
		t.Fatalf("interrupt turn id=%q", interruptTurnID)
	}
}

func TestHubModelDirectModelCommandSplitsProviderQualifiedModel(t *testing.T) {
	var gotModel appwire.ThreadModelSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
			gotModel = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("/model openai/gpt-5-mini")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("direct /model provider/model returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if gotModel.Ref != "local:01SEND" || gotModel.ModelProvider != "openai" || gotModel.Model != "gpt-5-mini" {
		t.Fatalf("model set params=%+v, want local:01SEND openai/gpt-5-mini", gotModel)
	}
	if view := m.View(); !strings.Contains(view, "Model updated.") {
		t.Fatalf("missing model updated message:\n%s", view)
	}
}

func TestHubModelSessionModelPickerUsesHubModels(t *testing.T) {
	var gotList appwire.ModelListParams
	var gotModel appwire.ThreadModelSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
			gotList = params
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
				{Provider: "openai", Model: "gpt-5"},
				{Provider: "openai", Model: "gpt-5-mini"},
			}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
			gotModel = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.WorkingDir = "/tmp/serf"
	m.detail.Model = "openai/gpt-5"
	m.session.setInputValue("/model")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("/model should fetch models instead of showing usage:\n%s", updated.(hubModel).View())
	}
	updated, cmd = updated.(hubModel).Update(cmd())
	if cmd != nil {
		t.Fatal("model list result should not return another command")
	}
	m = updated.(hubModel)
	if gotList.CWD != "/tmp/serf" {
		t.Fatalf("model list cwd=%q, want /tmp/serf", gotList.CWD)
	}
	if m.sessionModelPicker == nil {
		t.Fatalf("expected session model picker:\n%s", m.View())
	}
	if got := m.View(); !strings.Contains(got, "Select model") || !strings.Contains(got, "openai/gpt-5-mini") {
		t.Fatalf("model picker view missing model choices:\n%s", got)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("mini")})
	if cmd != nil {
		t.Fatal("filtering model picker should be synchronous")
	}
	updated, cmd = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("selecting a model should call thread/model/set")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)

	if gotModel.Ref != "local:01SEND" || gotModel.ModelProvider != "openai" || gotModel.Model != "gpt-5-mini" {
		t.Fatalf("model set params=%+v, want local:01SEND openai/gpt-5-mini", gotModel)
	}
	if m.sessionModelPicker != nil {
		t.Fatal("session model picker should close after selection")
	}
	if got := m.View(); !strings.Contains(got, "Model updated.") {
		t.Fatalf("missing model updated message:\n%s", got)
	}
}

func TestHubModelSessionModelPickerDisablesAuthRequiredModels(t *testing.T) {
	var setCalls int
	var gotModel appwire.ThreadModelSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{
					{Provider: "openai", Model: "gpt-5"},
					{Provider: "ollama", Model: "llama3"},
				},
				Diagnostics: []appwire.ModelListDiagnostic{{
					Provider: "openai",
					Title:    "Login required",
					Message:  "OpenAI login required",
					Hint:     "run /auth openai",
				}},
			}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
			setCalls++
			gotModel = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.WorkingDir = "/tmp/serf"
	m.detail.Model = "ollama/llama3"
	m.session.setInputValue("/model")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("/model should fetch models:\n%s", updated.(hubModel).View())
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if m.sessionModelPicker == nil {
		t.Fatalf("expected session model picker:\n%s", m.View())
	}
	if view := m.View(); !strings.Contains(view, "openai/gpt-5") || !strings.Contains(view, "disabled: Login required") || !strings.Contains(view, "/auth openai") {
		t.Fatalf("missing disabled auth-required model row:\n%s", view)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("disabled model selection returned unexpected command")
	}
	m = updated.(hubModel)
	if setCalls != 0 {
		t.Fatalf("disabled model selection called thread/model/set %d time(s)", setCalls)
	}
	if m.sessionModelPicker == nil {
		t.Fatal("disabled model selection should keep picker open")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, cmd = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enabled model selection should call thread/model/set")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)
	if gotModel.Ref != "local:01SEND" || gotModel.ModelProvider != "ollama" || gotModel.Model != "llama3" {
		t.Fatalf("model set params=%+v, want local:01SEND ollama/llama3", gotModel)
	}
	if m.sessionModelPicker != nil {
		t.Fatal("enabled selection should close picker")
	}
}

func TestHubModelSessionModelPickerRequiresCapability(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.ChangeModel = false
	m.session.setInputValue("/model")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("unavailable model picker should not fetch models")
	}
	got := updated.(hubModel)
	if got.sessionModelPicker != nil {
		t.Fatal("unavailable model picker should stay closed")
	}
	if view := got.View(); !strings.Contains(view, "Model change is not available for this session.") {
		t.Fatalf("missing unavailable model message:\n%s", view)
	}
}

func TestHubModelThemePicker(t *testing.T) {
	previous := currentThemeName()
	t.Cleanup(func() {
		setTheme(previous)
	})
	setTheme("light")

	m := newSessionHubModel(nil)
	m.session.setInputValue("/theme")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/theme should not need an async command")
	}
	m = updated.(hubModel)
	if m.sessionThemePicker == nil {
		t.Fatalf("expected theme picker:\n%s", m.View())
	}
	if got := m.View(); !strings.Contains(got, "Select theme") || !strings.Contains(got, "dark") || !strings.Contains(got, "light") {
		t.Fatalf("theme picker view missing choices:\n%s", got)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmd != nil {
		t.Fatal("theme picker navigation should be synchronous")
	}
	updated, cmd = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("theme picker selection should be synchronous")
	}
	m = updated.(hubModel)
	if m.sessionThemePicker != nil {
		t.Fatal("theme picker should close after selection")
	}
	if currentThemeName() != "dark" {
		t.Fatalf("theme=%q, want dark", currentThemeName())
	}
	if got := m.View(); !strings.Contains(got, "Switched to dark theme.") {
		t.Fatalf("missing theme switch message:\n%s", got)
	}
}

func TestHubModelThemePickerPersistsStateDirPreference(t *testing.T) {
	previous := currentThemeName()
	t.Cleanup(func() {
		setTheme(previous)
	})

	stateDir := t.TempDir()
	setTheme("light")
	m := newHubModel(nil, "http://hub.test", stateDir)
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:01SEND", SessionID: "01SEND", Capabilities: hubSessionCapabilities{Send: true}}
	m.session.setInputValue("/theme")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/theme should not need an async command")
	}
	updated, cmd = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmd != nil {
		t.Fatal("theme picker navigation should be synchronous")
	}
	updated, cmd = updated.(hubModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("theme picker selection should be synchronous")
	}

	if got, ok := loadThemePreference(stateDir); !ok || got != "dark" {
		t.Fatalf("stored theme=%q ok=%v, want dark", got, ok)
	}
	if currentThemeName() != "dark" {
		t.Fatalf("theme=%q, want dark", currentThemeName())
	}
}

func TestHubModelTurnStartEnablesTurnActions(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.ActiveTurnID = ""
	m.detail.Capabilities.Interrupt = false
	m.detail.Capabilities.Steer = false

	updated, _ := m.Update(hubSendMsg{text: "hello", turnID: "turn_started"})
	got := updated.(hubModel)
	if got.detail.ActiveTurnID != "turn_started" {
		t.Fatalf("active turn id=%q", got.detail.ActiveTurnID)
	}
	if got.detail.Capabilities.Interrupt || got.detail.Capabilities.Steer {
		t.Fatalf("turn/start mutated unsupported turn actions: %+v", got.detail.Capabilities)
	}

	got.detail.ActiveTurnID = ""
	got.detail.Capabilities.Interrupt = false
	got.detail.Capabilities.Steer = false
	params, err := json.Marshal(map[string]any{
		"turn": appwire.Turn{ID: "turn_notified", Status: appwire.TurnStatusInProgress},
	})
	if err != nil {
		t.Fatal(err)
	}
	got.applyHubNotification(appwire.Notification{Method: appwire.NotifyTurnStarted, Params: params})
	if got.detail.ActiveTurnID != "turn_notified" {
		t.Fatalf("notified active turn id=%q", got.detail.ActiveTurnID)
	}
	if got.detail.Capabilities.Interrupt || got.detail.Capabilities.Steer {
		t.Fatalf("turn/started mutated unsupported turn actions: %+v", got.detail.Capabilities)
	}
}

func TestHubModelIgnoresAsyncCompletionForDifferentSession(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Ref = "local:current"
	m.detail.ActiveTurnID = "turn_current"
	m.session.setInputValue("current draft")
	m.sessionQueue = []string{"current queued"}

	updated, _ := m.Update(hubSendMsg{ref: "local:old", text: "old", draft: "old draft", turnID: "turn_old"})
	got := updated.(hubModel)
	if got.detail.ActiveTurnID != "turn_current" {
		t.Fatalf("stale send changed active turn id=%q", got.detail.ActiveTurnID)
	}
	if got.session.input.Value() != "current draft" {
		t.Fatalf("stale send changed draft=%q", got.session.input.Value())
	}

	updated, _ = got.Update(hubQueueMsg{ref: "local:old", text: "old", draft: "old draft", err: errors.New("queue failed")})
	got = updated.(hubModel)
	if got.session.input.Value() != "current draft" {
		t.Fatalf("stale queue error restored old draft=%q", got.session.input.Value())
	}

	updated, _ = got.Update(hubDrainAsSteerMsg{
		ref:           "local:old",
		text:          "old queued",
		draft:         "old draft",
		preQueueDepth: 1,
		err:           appwire.Conflict("queued before drain failed"),
		queued:        true,
	})
	got = updated.(hubModel)
	if len(got.sessionQueue) != 1 || got.sessionQueue[0] != "current queued" {
		t.Fatalf("stale drain mutated queue preview: %+v", got.sessionQueue)
	}
	if got.session.input.Value() != "current draft" {
		t.Fatalf("stale drain changed draft=%q", got.session.input.Value())
	}
}

func TestHubModelSendErrorRemovesOptimisticUserEcho(t *testing.T) {
	m := newSessionHubModel(nil)
	reducer := m.sessionTranscriptReducer()
	reducer.applyUserMessageEcho("ship it")
	m.applySessionTranscriptReducer(reducer)
	m.session.messages = append(m.session.messages, chatMessage{
		Kind:      msgUser,
		Text:      "ship it",
		Failed:    true,
		PendingID: 42,
	})

	updated, _ := m.Update(hubSendMsg{text: "ship it", draft: "ship it", err: errors.New("network down")})
	got := updated.(hubModel)

	failedRows := 0
	for _, msg := range got.session.messages {
		if msg.Kind != msgUser || msg.Text != "ship it" {
			continue
		}
		if msg.Failed && msg.PendingID == 42 {
			failedRows++
			continue
		}
		if !msg.Failed && msg.PendingID == 0 {
			t.Fatalf("failed send left optimistic user echo: %+v", got.session.messages)
		}
	}
	if failedRows != 1 {
		t.Fatalf("failed pending placeholder count=%d messages=%+v", failedRows, got.session.messages)
	}
	if got.session.input.Value() != "ship it" {
		t.Fatalf("draft=%q, want restored draft", got.session.input.Value())
	}
}

func TestHubModelBrowseForkDraftPostsForkAndNavigatesToChild(t *testing.T) {
	var gotReq appwire.ThreadForkParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadFork, func(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
			gotReq = params
			return appwire.ThreadForkResponse{Thread: appwireThread(hubTreeNode{Ref: "local:02CHILD", SessionID: "02CHILD", Title: "child", State: "idle", Project: "serf", Live: true}, "/tmp/serf")}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwireThread(hubTreeNode{Ref: "local:02CHILD", SessionID: "02CHILD", Title: "child", State: "idle", Project: "serf", Live: true}, "/tmp/serf")}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.Capabilities.Fork = true
	m.session.messages = []chatMessage{
		{Kind: msgUser, Text: "original request", TurnIndex: 3},
		{Kind: msgAssistant, Text: "answer"},
	}
	m.session.scrollMode = true
	m.browseSelected = 0

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd != nil {
		t.Fatal("starting a fork draft should be synchronous")
	}
	draft := updated.(hubModel)
	if draft.forkDraft == nil || draft.forkDraft.Turn != 3 {
		t.Fatalf("fork draft=%+v", draft.forkDraft)
	}
	if draft.session.scrollMode {
		t.Fatal("fork draft should return to compose focus")
	}
	if draft.session.input.Value() != "original request" {
		t.Fatalf("draft input=%q", draft.session.input.Value())
	}

	draft.session.setInputValue("edited request")
	updated, cmd = draft.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("confirming fork draft should post to hub")
	}
	updated, cmd = updated.(hubModel).Update(cmd())
	if cmd == nil {
		t.Fatal("successful fork should fetch child session")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)
	if got.detail.SessionID != "02CHILD" {
		t.Fatalf("detail=%+v", got.detail)
	}
	if gotReq.Ref != "local:01SEND" || gotReq.SourceTurnID != "3" || gotReq.EditedInput != "edited request" || gotReq.Label != "original before fork" {
		t.Fatalf("fork request=%+v", gotReq)
	}
}

func TestHubModelBrowseForkRequiresUserTurnWithTurnIndex(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true
	m.session.messages = []chatMessage{{Kind: msgUser, Text: "not persisted"}}
	m.session.scrollMode = true
	m.browseSelected = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	got := updated.(hubModel)
	if got.forkDraft != nil {
		t.Fatalf("fork draft=%+v, want nil", got.forkDraft)
	}
	view := got.View()
	if !strings.Contains(view, "fork requires persisted transcript") {
		t.Fatalf("missing fork reason:\n%s", view)
	}
}

func TestHubModelBrowseForkRequiresSelectedUserMessage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true
	m.session.messages = []chatMessage{
		{Kind: msgAssistant, Text: "assistant answer"},
		{Kind: msgUser, Text: "forkable request", TurnIndex: 1},
	}
	m.session.scrollMode = true
	m.browseSelected = 0

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd != nil {
		t.Fatal("invalid fork selection should not call hub")
	}
	got := updated.(hubModel)
	if got.forkDraft != nil {
		t.Fatalf("fork draft=%+v, want nil", got.forkDraft)
	}
	if !strings.Contains(got.View(), "Select a user turn to fork.") {
		t.Fatalf("missing invalid selection reason:\n%s", got.View())
	}
}

func TestHubModelForkFailurePreservesDraftAndLabel(t *testing.T) {
	var gotReq appwire.ThreadForkParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadFork, func(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
			gotReq = params
			return appwire.ThreadForkResponse{}, fmt.Errorf("fork failed from test")
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.Capabilities.Fork = true
	m.session.messages = []chatMessage{{Kind: msgUser, Text: "original request", TurnIndex: 3}}
	m.session.scrollMode = true
	m.browseSelected = 0

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd != nil {
		t.Fatal("starting a fork draft should be synchronous")
	}
	draft := updated.(hubModel)
	draft.session.setInputValue("edited request")

	updated, cmd = draft.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("confirming fork draft should post to hub")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	got := updated.(hubModel)
	if got.forkDraft == nil {
		t.Fatal("failed fork should keep the draft active")
	}
	if got.forkDraft.Label != "original before fork" {
		t.Fatalf("draft label=%q", got.forkDraft.Label)
	}
	if got.session.input.Value() != "edited request" {
		t.Fatalf("draft input=%q, want edited request", got.session.input.Value())
	}
	view := got.View()
	if !strings.Contains(view, "Fork failed:") || !strings.Contains(view, "fork failed from test") {
		t.Fatalf("missing fork failure notice:\n%s", view)
	}
	if gotReq.Ref != "local:01SEND" || gotReq.SourceTurnID != "3" || gotReq.EditedInput != "edited request" || gotReq.Label != "original before fork" {
		t.Fatalf("fork request=%+v", gotReq)
	}
}

func TestHubModelDashboardShowsRecentWhenNothingLive(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01ENDED", SessionID: "01ENDED", Title: "ended history", State: "ended", Project: "serf"},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	got := m.dashboardView()
	for _, want := range []string{"0 live", "0 live · 1 recent", "1 recent", "filter"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard recent-only state missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ended history") {
		t.Fatalf("recent-only dashboard should fold ended sessions by default:\n%s", got)
	}
	m.selected = 2
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if expanded := updated.(hubModel).dashboardView(); !strings.Contains(expanded, "ended history") {
		t.Fatalf("recent disclosure did not reveal ended session:\n%s", expanded)
	}
	if strings.Contains(got, "s start") {
		t.Fatalf("dashboard recent-only state advertised legacy s key:\n%s", got)
	}
}

func TestHubModelDashboardCanExpandRecentOnlyProject(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "serf",
		Name:       "serf",
		WorkingDir: "/tmp/serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01ENDED", SessionID: "01ENDED", Title: "ended history", State: "ended", Project: "serf"},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 2

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("recent disclosure should be synchronous")
	}
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("recent disclosure mode=%v, want dashboard", got.mode)
	}
	if view := got.dashboardView(); !strings.Contains(view, "ended history") {
		t.Fatalf("recent disclosure did not render ended session:\n%s", view)
	}
}

func TestHubModelActionBarsUseApprovedNewSessionKey(t *testing.T) {
	corpus := newHubTUISampleCorpus()
	m := newHubModel(nil, "http://hub.test")
	m.tree = corpus.DashboardTree
	m.rows = buildDashboardRows(corpus.DashboardTree)
	if got := m.dashboardView(); !strings.Contains(got, "new") || strings.Contains(got, "s spawn") || strings.Contains(got, "p project") {
		t.Fatalf("dashboard action bar should advertise new without legacy project/spawn keys:\n%s", got)
	}
}

func dashboardRowLabels(rows []hubRow) []string {
	labels := make([]string, 0, len(rows))
	for _, row := range rows {
		switch row.kind {
		case hubRowProject:
			labels = append(labels, "project:"+row.project)
		case hubRowSession:
			labels = append(labels, "session:"+row.title)
		}
	}
	return labels
}

func dashboardRowIndex(rows []hubRow, title string) int {
	for i, row := range rows {
		if row.title == title {
			return i
		}
	}
	return 0
}

func TestHubModelSpawnFormDoesNotAdvertiseOrAcceptCtrlS(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnDir = "/tmp/serf"
	m.spawnHarness = "serf"
	m.spawnHarnesses = []string{"serf"}
	m.spawnHarnessKinds = map[string]string{"serf": "serf"}
	m.spawnModel = "openai/gpt-5"
	m.session.setInputValue("draft")

	if got := m.spawnView(); strings.Contains(got, "ctrl+s") {
		t.Fatalf("spawn form advertised ctrl+s compatibility shortcut:\n%s", got)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil {
		t.Fatal("ctrl+s should not submit spawn")
	}
	if got := updated.(hubModel); got.mode != hubModeSpawn || got.session.input.Value() != "draft" {
		t.Fatalf("ctrl+s should leave spawn draft in place: mode=%v draft=%q", got.mode, got.session.input.Value())
	}
}

func TestHubModelSessionFooterShowsBrowseAndDashboardKeys(t *testing.T) {
	m := newSessionHubModel(nil)
	got := m.sessionView()
	for _, want := range []string{"esc: browse", "ctrl+o: dashboard"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session footer missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "enter: send") {
		t.Fatalf("session footer missing send key:\n%s", got)
	}
	if strings.Contains(got, "esc: dashboard") {
		t.Fatalf("session footer still advertises esc dashboard:\n%s", got)
	}

	m.detail.Capabilities.Send = false
	got = m.sessionView()
	if strings.Contains(got, "enter: send") {
		t.Fatalf("session footer advertised unavailable send:\n%s", got)
	}

	m.detail.Capabilities.Send = true
	m.detail.Capabilities.Fork = true
	m.enterSessionBrowse(false)
	got = m.sessionView()
	for _, want := range []string{"esc/i/q: compose", "f: fork selected user turn", "ctrl+o: dashboard"} {
		if !strings.Contains(got, want) {
			t.Fatalf("browse footer missing %q:\n%s", want, got)
		}
	}
	m.detail.Capabilities.Fork = false
	got = m.sessionView()
	if strings.Contains(got, "f: fork") {
		t.Fatalf("browse footer advertised unavailable fork:\n%s", got)
	}
}

func TestHubModelForkCommandEntersBrowseMode(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true
	m.session.messages = []chatMessage{
		{Kind: msgUser, Text: "original request", TurnIndex: 1},
		{Kind: msgAssistant, Text: "answer", TurnIndex: 1},
	}
	m.session.refreshViewport()
	m.session.setInputValue("/fork")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/fork should enter browse mode synchronously")
	}
	got := updated.(hubModel)
	if !got.session.scrollMode {
		t.Fatal("/fork should enter transcript browse mode")
	}
	view := got.sessionView()
	for _, want := range []string{"Select a user turn, then press f to fork.", "f: fork selected user turn", "original request"} {
		if !strings.Contains(view, want) {
			t.Fatalf("/fork view missing %q:\n%s", want, view)
		}
	}
}

func TestHubModelHelpRespectsSessionCapabilities(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities = hubSessionCapabilities{
		Send:    true,
		Compact: true,
	}

	m.session.setInputValue("/help")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/help should not need an async command")
	}
	got := updated.(hubModel).View()
	if !strings.Contains(got, "/compact") {
		t.Fatalf("help missing supported compact:\n%s", got)
	}
	for _, unavailable := range []string{"/model", "/clear", "/fork", "/shutdown"} {
		if strings.Contains(got, unavailable) {
			t.Fatalf("help advertised unavailable command %q:\n%s", unavailable, got)
		}
	}

	m = newSessionHubModel(nil)
	m.detail.Capabilities = hubSessionCapabilities{
		Shutdown: true,
	}
	m.session.setInputValue("/help")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/help should not need an async command")
	}
	got = updated.(hubModel).View()
	if !strings.Contains(got, "/shutdown") {
		t.Fatalf("help missing supported shutdown:\n%s", got)
	}

	m = newSessionHubModel(nil)
	m.detail.Capabilities = hubSessionCapabilities{
		Fork: true,
	}
	m.session.setInputValue("/help")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/help should not need an async command")
	}
	got = updated.(hubModel).View()
	if !strings.Contains(got, "/fork") || !strings.Contains(got, "Fork selected user turn") {
		t.Fatalf("help missing supported fork:\n%s", got)
	}
}

func TestHubModelHelpAndPaletteShareSessionCommands(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities = hubSessionCapabilities{
		Send:        true,
		Steer:       true,
		Interrupt:   true,
		Compact:     true,
		Clear:       true,
		Fork:        true,
		Shutdown:    true,
		ChangeModel: true,
	}

	help := hubSlashCommandHelp(m.detail.Capabilities)
	m.openCommandPalette()
	if m.commandPalette == nil {
		t.Fatal("session palette did not open")
	}
	palette := m.commandPalette.View()

	for _, command := range []string{"/help", "/tasks", "/agents", "/auth", "/login", "/logout", "/model", "/clear", "/fork", "/shutdown", "/theme", "/dashboard", "/project"} {
		if !strings.Contains(help, command) {
			t.Fatalf("help missing command %q:\n%s", command, help)
		}
		if !strings.Contains(palette, command) {
			t.Fatalf("palette missing command %q:\n%s", command, palette)
		}
	}
}

func TestHubModelSessionCommandPaletteDoesNotShowOtherSessions(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Project = "serf"
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key: "serf", Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "Please reply exactly: old smoke", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01PAST", SessionID: "01PAST", Title: "past renderer", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	m.openCommandPalette()
	if m.commandPalette == nil {
		t.Fatal("session palette did not open")
	}
	got := m.sessionView()
	if !strings.Contains(got, "Command palette") || !strings.Contains(got, "/help") {
		t.Fatalf("session command palette missing commands:\n%s", got)
	}
	for _, unwanted := range []string{"Please reply exactly: old smoke", "past renderer", "/search"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("session command palette should not show cross-session search entry %q:\n%s", unwanted, got)
		}
	}
}

func TestHubModelSessionPaletteShowsDisabledCommandReasons(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities = hubSessionCapabilities{Send: true}

	m.openCommandPalette()
	if m.commandPalette == nil {
		t.Fatal("session palette did not open")
	}
	got := m.commandPalette.View()
	for _, want := range []string{
		"/model",
		"disabled: source does not advertise change model",
		"/clear",
		"disabled: source does not advertise clear",
		"/fork",
		"disabled: source does not advertise fork",
		"/shutdown",
		"disabled: source does not advertise shutdown",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("session palette missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelDashboardPaletteCanSearchOtherSessions(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key: "serf", Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live scoring", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01PAST", SessionID: "01PAST", Title: "past renderer", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	m.openCommandPalette()
	if m.commandPalette == nil {
		t.Fatal("dashboard palette did not open")
	}
	for _, r := range "past" {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd != nil {
			t.Fatal("filtering dashboard palette should be synchronous")
		}
		m = updated.(hubModel)
	}
	got := m.dashboardView()
	if !strings.Contains(got, "Command palette") || !strings.Contains(got, "past renderer") {
		t.Fatalf("dashboard palette should search sessions:\n%s", got)
	}
	if strings.Contains(got, "live scoring") {
		t.Fatalf("dashboard session search should honor palette filter:\n%s", got)
	}
}

func TestHubModelSearchCommandIsNotASessionCommand(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Project = "serf"
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key: "serf", Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live scoring", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01PAST", SessionID: "01PAST", Title: "past renderer", State: "ended", Project: "serf", Live: false},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.session.setInputValue("keep this draft")

	cmd := m.runHubSlashCommand("search", "")
	if cmd != nil {
		t.Fatal("/search should not run an async session command")
	}
	if m.commandPalette != nil {
		t.Fatal("/search should not open cross-session search from a session")
	}
	if got := m.session.input.Value(); got != "keep this draft" {
		t.Fatalf("/search should not replace composer draft, got %q", got)
	}
	if got := m.sessionView(); !strings.Contains(got, "Unknown command: /search") {
		t.Fatalf("/search should not be advertised as a session command:\n%s", got)
	}
}

func TestHubModelSessionPaletteShortcutDoesNotExposeCrossSessionSearch(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Project = "serf"
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key: "serf", Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live scoring", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01PAST", SessionID: "01PAST", Title: "past renderer", State: "ended", Project: "serf", Live: false},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.session.setInputValue("keep this draft")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if cmd != nil {
		t.Fatal("opening session palette should be synchronous")
	}
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "keep this draft" {
		t.Fatalf("palette shortcut replaced composer draft with %q", got)
	}
	if m.commandPalette == nil {
		t.Fatal("ctrl+p did not open session command palette")
	}
	if got := m.sessionView(); !strings.Contains(got, "/help") || strings.Contains(got, "/search") || strings.Contains(got, "live scoring") || !strings.Contains(got, "> keep this draft") {
		t.Fatalf("session palette should show commands, not cross-session search:\n%s", got)
	}

	for _, r := range "model" {
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd != nil {
			t.Fatal("filtering session palette should be synchronous")
		}
		m = updated.(hubModel)
	}
	got := m.sessionView()
	for _, want := range []string{"Command palette", "/model", "> keep this draft"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session command palette missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSessionLeadingSlashOpensCommandPalette(t *testing.T) {
	m := newSessionHubModel(nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if cmd != nil {
		t.Fatal("opening session slash palette should be synchronous")
	}
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "" {
		t.Fatalf("leading slash should open discovery instead of staying in composer, input=%q", got)
	}
	if m.commandPalette == nil {
		t.Fatal("leading slash did not open session command palette")
	}
	got := m.sessionView()
	if !strings.Contains(got, "Command palette") || !strings.Contains(got, "/help") || !strings.Contains(got, "/model") {
		t.Fatalf("session slash palette missing commands:\n%s", got)
	}
}

func TestHubModelCtrlCQuitsWithoutInAppWarningFromSession(t *testing.T) {
	m := newSessionHubModel(nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatal("first ctrl+c should arm quit without quitting")
	}
	m = updated.(hubModel)
	got := m.View()
	for _, unwanted := range []string{"Press ctrl+c again", "Restore this session:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("first ctrl+c should not render in-app quit warning %q:\n%s", unwanted, got)
		}
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if got := updated.(hubModel); got.commandPalette != nil {
		t.Fatalf("second ctrl+c should quit instead of opening/keeping palette: %+v", got.commandPalette)
	}
	requireQuitCommand(t, cmd)
}

func TestHubModelSecondCtrlCStoresPostQuitRestoreMessage(t *testing.T) {
	m := newSessionHubModel(nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(hubModel)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := updated.(hubModel)

	requireQuitCommand(t, cmd)
	if got.postQuitMessage == "" {
		t.Fatal("second ctrl+c did not preserve a post-quit restore message")
	}
	for _, want := range []string{"Restore this session:", "serf-tui --hub-addr http://hub.test", "local:01SEND"} {
		if !strings.Contains(got.postQuitMessage, want) {
			t.Fatalf("post-quit restore message missing %q: %q", want, got.postQuitMessage)
		}
	}
	if strings.Contains(got.postQuitMessage, "Press ctrl+c again") {
		t.Fatalf("post-quit message should only contain restoration instructions: %q", got.postQuitMessage)
	}
}

func TestHubModelCtrlCWarningExpires(t *testing.T) {
	m := newSessionHubModel(nil)
	m.lastCtrlC = time.Now().Add(-2 * time.Second)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatal("expired ctrl+c warning should not quit")
	}
	got := updated.(hubModel).View()
	for _, unwanted := range []string{"Press ctrl+c again", "Restore this session:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("expired first ctrl+c should not render in-app warning %q:\n%s", unwanted, got)
		}
	}
}

func TestHubModelCtrlCQuitsOutsideSessionButArmsQuitInSession(t *testing.T) {
	cases := []struct {
		name  string
		model hubModel
		quits bool
	}{
		{name: "dashboard", model: newHubModel(nil, "http://hub.test"), quits: true},
		{name: "spawn", model: func() hubModel {
			m := newHubModel(nil, "http://hub.test")
			m.openSpawnForm()
			return m
		}(), quits: true},
		{name: "palette", model: func() hubModel {
			m := sampleHubModel(100)
			m.openCommandPalette()
			return m
		}(), quits: true},
		{name: "session", model: newSessionHubModel(nil), quits: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updated, cmd := tc.model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if tc.quits {
				requireQuitCommand(t, cmd)
				return
			}
			if cmd != nil {
				t.Fatal("first session ctrl+c should arm quit without quitting")
			}
			if got := updated.(hubModel).View(); strings.Contains(got, "Press ctrl+c again") {
				t.Fatalf("session ctrl+c rendered in-app warning:\n%s", got)
			}
		})
	}
}

func TestHubModelUnknownSlashCommandIncludesHelpHint(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("/wat")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("unknown command should not need an async command")
	}
	got := updated.(hubModel).View()
	for _, want := range []string{"Unknown command: /wat", "/help"} {
		if !strings.Contains(got, want) {
			t.Fatalf("unknown command output missing %q:\n%s", want, got)
		}
	}
}

func requireQuitCommand(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected quit command, got nil")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestHubDetailFromThreadIncludesActiveTurnID(t *testing.T) {
	detail := hubDetailFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:th_1"},
		Turns: []appwire.Turn{
			{ID: "turn_done", Status: appwire.TurnStatusCompleted},
			{ID: "turn_active", Status: appwire.TurnStatusInProgress},
		},
	})
	if detail.ActiveTurnID != "turn_active" {
		t.Fatalf("active turn id=%q", detail.ActiveTurnID)
	}
}

func newSessionHubModel(client *appwire.Client) hubModel {
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{
		Ref:          "local:01SEND",
		SessionID:    "01SEND",
		Title:        "send task",
		State:        "idle",
		ActiveTurnID: "turn_active",
		Capabilities: hubSessionCapabilities{
			Send:        true,
			Interrupt:   true,
			Compact:     true,
			Clear:       true,
			ChangeModel: true,
		},
	}
	m.session.sessionID = "01SEND"
	return m
}

func renderedLineCount(view string) int {
	view = strings.TrimRight(view, "\n")
	if view == "" {
		return 0
	}
	return strings.Count(view, "\n") + 1
}

func TestHubModelIgnoresNotificationsForOtherSessions(t *testing.T) {
	m := hubModel{
		mode: hubModeSession,
		detail: hubSessionDetail{
			Ref:       "local:current",
			SessionID: "current",
		},
		session: newModel("", "", nil),
	}

	m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "other",
		Ref:      "local:other",
		Delta:    "wrong",
	}).Notification)
	if len(m.session.messages) != 0 {
		t.Fatalf("messages=%+v, want no mutation from other session", m.session.messages)
	}

	m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "current",
		Ref:      "local:current",
		Delta:    "right",
	}).Notification)
	if len(m.session.messages) != 1 || m.session.messages[0].Text != "right" {
		t.Fatalf("messages=%+v", m.session.messages)
	}
}

func TestHubModelIgnoresPendingCoordinatorMessagesForOtherSessions(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Ref = "local:current"
	m.session.sessionID = "current"

	updated, _ := m.Update(pendingRegisteredMsg{entry: pendingEntry{
		ID:      1,
		Method:  appwire.MethodTurnStart,
		Text:    "wrong",
		Ref:     "local:other",
		Pending: true,
	}})
	got := updated.(hubModel)
	if len(got.session.messages) != 0 {
		t.Fatalf("messages=%+v, want no placeholder from other session", got.session.messages)
	}

	updated, _ = got.Update(pendingRegisteredMsg{entry: pendingEntry{
		ID:      2,
		Method:  appwire.MethodTurnStart,
		Text:    "right",
		Ref:     "local:current",
		Pending: true,
	}})
	got = updated.(hubModel)
	if len(got.session.messages) != 1 || got.session.messages[0].Text != "right" {
		t.Fatalf("messages=%+v", got.session.messages)
	}

	updated, _ = got.Update(pendingFailedMsg{
		entry: pendingEntry{
			ID:     2,
			Method: appwire.MethodTurnStart,
			Text:   "right",
			Ref:    "local:other",
		},
		reason: "wrong session",
	})
	got = updated.(hubModel)
	if got.session.messages[0].Failed {
		t.Fatalf("pending message failed from other session: %+v", got.session.messages[0])
	}
}

func newTestHubClient(t *testing.T, register func(*appserver.Server)) (*appwire.Client, func()) {
	t.Helper()
	app := appserver.NewServer(appserver.ServerConfig{
		ServerName: "serf-hub",
		SourceID:   "local",
		Features:   appwire.FeatureSet{},
	})
	appserver.HandleTyped(app.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: []appwire.HarnessDescriptor{{ID: "serf", Label: "serf", Kind: "serf"}}}, nil
	})
	if register != nil {
		register(app)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		app.ServeWebSocket(w, r)
	}))
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+srv.URL[len("http"):]+"/rpc", srv.Client())
	if err != nil {
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		_ = client.Close()
		srv.Close()
		t.Fatalf("initialize: %v", err)
	}
	return client, func() {
		_ = client.Close()
		srv.Close()
	}
}

func threadListResponse(tree hubTreeResponse) appwire.ThreadListResponse {
	seen := map[string]bool{}
	var data []appwire.Thread
	add := func(node hubTreeNode, cwd string) {
		if node.Ref == "" || seen[node.Ref] {
			return
		}
		seen[node.Ref] = true
		data = append(data, appwireThread(node, cwd))
	}
	for _, node := range tree.Live {
		add(node, "")
	}
	for _, project := range tree.Projects {
		for _, node := range project.Sessions {
			add(node, project.WorkingDir)
			for _, child := range node.Children {
				add(child, project.WorkingDir)
			}
		}
	}
	return appwire.ThreadListResponse{Data: data}
}

func appwireThread(node hubTreeNode, cwd string) appwire.Thread {
	ref, _ := appwire.ParseRef(node.Ref)
	if cwd == "" {
		project := node.Project
		if project == "" {
			project = "serf"
		}
		cwd = "/tmp/" + project
	}
	status := node.State
	if status == "" {
		if node.Live {
			status = appwire.ThreadStatusIdle
		} else {
			status = appwire.ThreadStatusNotLoaded
		}
	}
	if !node.Live && status == appwire.ThreadStatusIdle {
		status = appwire.ThreadStatusNotLoaded
	}
	threadID := ref.ThreadID
	if threadID == "" {
		threadID = node.SessionID
	}
	sessionID := node.SessionID
	if sessionID == "" {
		sessionID = threadID
	}
	title := node.Title
	if title == "" {
		title = sessionID
	}
	return appwire.Thread{
		ID:            threadID,
		SessionID:     sessionID,
		Preview:       title,
		Name:          title,
		ModelProvider: node.Model,
		CWD:           cwd,
		Source:        ref.SourceID,
		Status:        appwire.ThreadStatus{Type: status},
		Serf: appwire.SerfThread{
			Ref: node.Ref,
			Capabilities: appwire.ThreadCapabilities{
				Send:         true,
				Steer:        true,
				Interrupt:    true,
				Compact:      true,
				Clear:        true,
				ForkFromTurn: true,
				Shutdown:     true,
				ChangeModel:  true,
			},
		},
	}
}
