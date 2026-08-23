package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// TestFetchHubSpawnOptionsIncludesRecentDirs covers the terminal session
// creation flow's recent-project source (issue #35): spawn options carry the
// hub's most recently used project dirs for the Dir field's dropdown.
func TestFetchHubSpawnOptionsIncludesRecentDirs(t *testing.T) {
	recent := []string{"/proj/alpha", "/proj/beta"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
			return appwire.HarnessListResponse{Data: []appwire.HarnessDescriptor{{ID: "evener", Label: "evener"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerProjectsRecent, func(context.Context, appwire.ProjectsRecentParams) (appwire.ProjectsRecentResponse, error) {
			return appwire.ProjectsRecentResponse{Data: recent}, nil
		})
	})
	defer cleanup()

	msg := fetchHubSpawnOptions(client, "")()
	opts, ok := msg.(hubSpawnOptionsMsg)
	if !ok || opts.err != nil {
		t.Fatalf("msg=%T err=%v", msg, opts.err)
	}
	if len(opts.recentDirs) != 2 || opts.recentDirs[0] != "/proj/alpha" || opts.recentDirs[1] != "/proj/beta" {
		t.Fatalf("recentDirs=%v, want %v", opts.recentDirs, recent)
	}

	m := newHubModel(client, "http://hub.test")
	m.openSpawnForm()
	updated, _ := m.Update(msg)
	got := updated.(hubModel)
	if len(got.spawnRecentDirs) != 2 || got.spawnRecentDirs[0] != "/proj/alpha" {
		t.Fatalf("spawnRecentDirs=%v, want %v", got.spawnRecentDirs, recent)
	}
}

// TestHubModelSpawnDirTabCyclesRecentProjects: on an empty Dir field, tab
// prepopulates the most recent project; repeated tabs cycle the list.
func TestHubModelSpawnDirTabCyclesRecentProjects(t *testing.T) {
	m := newHubModel(nil, "")
	m.openSpawnForm()
	m.spawnRecentDirs = []string{"/proj/alpha", "/proj/beta", "/proj/gamma"}
	m.setSpawnFocus(hubSpawnFieldDir)

	tab := func() hubModel {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(hubModel)
		return m
	}

	if got := tab(); got.spawnDir != "/proj/alpha" {
		t.Fatalf("first tab dir=%q, want /proj/alpha (most recent project)", got.spawnDir)
	}
	if got := tab(); got.spawnDir != "/proj/beta" {
		t.Fatalf("second tab dir=%q, want /proj/beta", got.spawnDir)
	}
	if got := tab(); got.spawnDir != "/proj/gamma" {
		t.Fatalf("third tab dir=%q, want /proj/gamma", got.spawnDir)
	}
	if got := tab(); got.spawnDir != "/proj/alpha" {
		t.Fatalf("fourth tab dir=%q, want wrap-around to /proj/alpha", got.spawnDir)
	}
	if m.spawnFocus != hubSpawnFieldDir {
		t.Fatalf("cycling recent projects should keep focus on the dir field, got %v", m.spawnFocus)
	}
}

// TestHubModelSpawnViewListsRecentProjects: the Dir field's dropdown lists
// the recent projects as options while the field shows one of them (or
// nothing), and hides them once the user types a custom path.
func TestHubModelSpawnViewListsRecentProjects(t *testing.T) {
	m := newHubModel(nil, "")
	m.openSpawnForm()
	m.spawnRecentDirs = []string{"/proj/alpha", "/proj/beta"}
	m.setSpawnFocus(hubSpawnFieldDir)

	view := m.spawnView()
	if !strings.Contains(view, "/proj/alpha") || !strings.Contains(view, "/proj/beta") {
		t.Fatalf("spawn view should list recent project options:\n%s", view)
	}

	m.setSpawnDir("/custom/path")
	view = m.spawnView()
	if strings.Contains(view, "/proj/alpha") {
		t.Fatalf("spawn view should hide recent options for a custom path:\n%s", view)
	}
}

// spawnFormWithProjectPrefill opens the spawn form with a real project row
// selected, so openSpawnForm prefills the Dir field the same way it does in
// the TUI (issue #51's repro path) rather than via a direct setSpawnDir call.
func spawnFormWithProjectPrefill(t *testing.T, workingDir string) hubModel {
	t.Helper()
	m := newHubModel(nil, "http://hub.test")
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:        "notrecent",
		Name:       "not-recent",
		WorkingDir: workingDir,
		Sessions: []hubTreeNode{
			{Ref: "local:01LIVE", SessionID: "01LIVE", Title: "live task", State: "idle", Project: "not-recent", Live: true},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	m.selected = 1 // the session row under the project, not the launch row
	m.openSpawnForm()
	if m.spawnDir != workingDir {
		t.Fatalf("spawnDir=%q, want %q (prefilled from selected row)", m.spawnDir, workingDir)
	}
	return m
}

// TestHubModelSpawnDirPrefillShowsRecentsUntilEdited covers issue #51: when
// the Dir field is prefilled from the selected project row, that prefill
// must not be treated like a user-typed custom path. Recents stay visible
// until the user actually edits the field, then hide (preserving the
// pinned custom-path behavior), and ctrl+u clearing restores visibility.
func TestHubModelSpawnDirPrefillShowsRecentsUntilEdited(t *testing.T) {
	m := spawnFormWithProjectPrefill(t, "/work/not-recent")
	m.spawnRecentDirs = []string{"/proj/alpha", "/proj/beta"}
	m.setSpawnFocus(hubSpawnFieldDir)

	if !m.spawnRecentDirsVisible() {
		t.Fatalf("recents should be visible while the prefill is untouched")
	}
	if view := m.spawnView(); !strings.Contains(view, "/proj/alpha") {
		t.Fatalf("spawn view should list recent options for an untouched prefill:\n%s", view)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(hubModel)
	if m.spawnRecentDirsVisible() {
		t.Fatalf("recents should hide once the user edits the prefilled dir")
	}
	if view := m.spawnView(); strings.Contains(view, "/proj/alpha") {
		t.Fatalf("spawn view should hide recent options after an edit:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(hubModel)
	if !m.spawnRecentDirsVisible() {
		t.Fatalf("ctrl+u clear should restore recents visibility")
	}
}

// TestHubModelSpawnDirPrefillTabCyclesIntoRecents covers issue #51: tab must
// reach the recent-projects list even when the Dir field holds the untouched
// open-time prefill, not just when it's empty.
func TestHubModelSpawnDirPrefillTabCyclesIntoRecents(t *testing.T) {
	m := spawnFormWithProjectPrefill(t, "/work/not-recent")
	m.spawnRecentDirs = []string{"/proj/alpha", "/proj/beta"}
	m.setSpawnFocus(hubSpawnFieldDir)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(hubModel)
	if m.spawnDir != "/proj/alpha" {
		t.Fatalf("tab from untouched prefill dir=%q, want /proj/alpha (first recent project)", m.spawnDir)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(hubModel)
	if m.spawnDir != "/proj/beta" {
		t.Fatalf("second tab dir=%q, want /proj/beta (cycling should continue normally after entering from the prefill)", m.spawnDir)
	}
}
