package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

func TestHubModelInitialFetchRendersRows(t *testing.T) {
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
	for _, want := range []string{"live task", "awaiting", "serf"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "past task") {
		t.Fatalf("dashboard rendered ended session:\n%s", got)
	}
}

func TestHubModelDashboardShowsOnlyLiveSessionsGroupedByProject(t *testing.T) {
	tree := hubTreeResponse{
		Live: []hubTreeNode{
			{Ref: "local:01LIVEA", SessionID: "01LIVEA", Title: "live alpha", State: "awaiting", Project: "serf", Live: true},
			{Ref: "local:01LIVEB", SessionID: "01LIVEB", Title: "live beta", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01BRAIN", SessionID: "01BRAIN", Title: "brain live", State: "processing", Project: "brainstorm", Live: true},
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
				RollupState: "processing",
				Sessions: []hubTreeNode{
					{Ref: "local:01BRAIN", SessionID: "01BRAIN", Title: "brain live", State: "processing", Project: "brainstorm", Live: true},
				},
			},
		},
	}

	rows := buildDashboardRows(tree)
	if len(rows) != 5 {
		t.Fatalf("rows=%d: %+v", len(rows), rows)
	}
	if rows[0].kind != hubRowProject || rows[0].project != "serf" {
		t.Fatalf("first row=%+v, want serf project header", rows[0])
	}
	if rows[1].kind != hubRowSession || rows[1].title != "live alpha" {
		t.Fatalf("second row=%+v, want live alpha session", rows[1])
	}
	if rows[3].kind != hubRowProject || rows[3].project != "brainstorm" {
		t.Fatalf("fourth row=%+v, want brainstorm project header", rows[3])
	}
	for _, row := range rows {
		if row.title == "ended history" {
			t.Fatalf("dashboard row included ended session: %+v", row)
		}
	}

	m := newHubModel(nil, "http://hub.test")
	m.tree = tree
	m.rows = rows
	got := m.dashboardView()
	for _, want := range []string{"serf", "live alpha", "live beta", "brainstorm", "brain live"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ended history") {
		t.Fatalf("dashboard rendered ended session:\n%s", got)
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

func TestHubModelDashboardFilterUsesFocusedInput(t *testing.T) {
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
		t.Fatal("opening dashboard filter should be synchronous")
	}
	m = updated.(hubModel)
	for _, r := range "beta" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(hubModel)
	}

	if got := m.dashboardFilter.Value(); got != "beta" {
		t.Fatalf("filter=%q, want beta", got)
	}
	view := m.dashboardView()
	if !strings.Contains(view, "filter: beta") || !strings.Contains(view, "beta task") {
		t.Fatalf("filtered dashboard missing active filter/beta row:\n%s", view)
	}
	if strings.Contains(view, "alpha task") {
		t.Fatalf("filtered dashboard still shows alpha row:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(hubModel)
	if m.dashboardFilter.Value() != "" {
		t.Fatalf("filter after esc=%q, want cleared", m.dashboardFilter.Value())
	}
	if view := m.dashboardView(); !strings.Contains(view, "alpha task") || !strings.Contains(view, "beta task") {
		t.Fatalf("dashboard did not restore rows after clearing filter:\n%s", view)
	}
}

func TestHubModelProjectViewShowsLiveThenRecent(t *testing.T) {
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

	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeProject
	m.tree = hubTreeResponse{Projects: []hubTreeProject{project}}
	m.selectedProjectKey = "serf"
	m.projectRows = rows
	got := m.projectView()
	for _, want := range []string{"serf / project / serf", "Live now", "Recent in this project", "live task", "ended history"} {
		if !strings.Contains(got, want) {
			t.Fatalf("project view missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "live task") > strings.Index(got, "ended history") {
		t.Fatalf("project view rendered ended before live:\n%s", got)
	}
}

func TestHubModelProjectFilterUsesFocusedInput(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live scoring", State: "idle", Project: "serf", Live: true},
			{Ref: "local:01PAST", SessionID: "01PAST", Title: "past renderer", State: "ended", Project: "serf", Live: false},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.openProject("serf")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(hubModel)
	for _, r := range "past" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(hubModel)
	}

	view := m.projectView()
	if !strings.Contains(view, "filter: past") || !strings.Contains(view, "past renderer") {
		t.Fatalf("filtered project missing active filter/past row:\n%s", view)
	}
	if strings.Contains(view, "live scoring") {
		t.Fatalf("filtered project still shows live row:\n%s", view)
	}
}

func TestHubModelDashboardProjectHeaderOpensProject(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("project header enter should not fetch a session")
	}
	got := updated.(hubModel)
	if got.mode != hubModeProject || got.selectedProjectKey != "serf" {
		t.Fatalf("mode=%v project=%q", got.mode, got.selectedProjectKey)
	}
	if !strings.Contains(got.View(), "serf / project / serf") {
		t.Fatalf("project view not rendered:\n%s", got.View())
	}
}

func TestHubModelProjectEscReturnsDashboard(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "serf",
		Name: "serf",
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "serf", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.openProject("serf")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
	if !strings.Contains(got.View(), "serf live") {
		t.Fatalf("dashboard not rendered:\n%s", got.View())
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
	if got.mode != hubModeProject || got.selectedProjectKey != "serf" {
		t.Fatalf("/project mode=%v project=%q", got.mode, got.selectedProjectKey)
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

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
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
	if gotSpawn.Prompt != "build the thing" {
		t.Fatalf("prompt=%q, want build the thing", gotSpawn.Prompt)
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

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
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
	updated, cmd = form.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
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

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
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

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
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
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "serf-hub", SourceID: "local"})
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

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
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
	if gotSpawn.Prompt != "slow spawn" {
		t.Fatalf("prompt=%q, want slow spawn", gotSpawn.Prompt)
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
	if got.Ref != "local:01SEND" || got.Prompt != "ship it" {
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
	if !strings.Contains(updated.(hubModel).View(), "/tmp/details") {
		t.Fatalf("details view missing:\n%s", updated.(hubModel).View())
	}
	if strings.Join(methods, ",") != appwire.MethodSerfTasksList+","+appwire.MethodThreadRead {
		t.Fatalf("methods=%v", methods)
	}
}

func TestHubModelActionsAndClearUseAppWire(t *testing.T) {
	var methods []string
	var interruptTurnID string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, func(_ context.Context, params appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
			methods = append(methods, appwire.MethodTurnInterrupt)
			interruptTurnID = params.TurnID
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
		"turn": appwire.Turn{ID: "turn_notified", Status: appwire.TurnStatusRunning},
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

func TestHubModelDashboardEmptyStateIsLiveOnly(t *testing.T) {
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
	for _, want := range []string{"No live sessions are running", "s start a session", "/projects browse project history"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard empty state missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ended history") {
		t.Fatalf("dashboard empty state rendered ended session:\n%s", got)
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
	for _, want := range []string{"esc/i/q: compose", "f: fork", "ctrl+o: dashboard"} {
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
	for _, unavailable := range []string{"/model", "/clear", "/shutdown"} {
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
}

func TestHubDetailFromThreadIncludesActiveTurnID(t *testing.T) {
	detail := hubDetailFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:th_1"},
		Turns: []appwire.Turn{
			{ID: "turn_done", Status: appwire.TurnStatusCompleted},
			{ID: "turn_active", Status: appwire.TurnStatusRunning},
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

func newTestHubClient(t *testing.T, register func(*appserver.Server)) (*appwire.Client, func()) {
	t.Helper()
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "serf-hub", SourceID: "local"})
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
			status = appwire.ThreadStatusEnded
		}
	}
	if !node.Live && status == appwire.ThreadStatusIdle {
		status = appwire.ThreadStatusEnded
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
