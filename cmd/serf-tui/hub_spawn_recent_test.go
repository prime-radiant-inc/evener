package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// TestFetchHubSpawnOptionsIncludesRecentDirs covers the terminal session
// creation flow's recent-project source (issue #35): spawn options carry the
// hub's most recently used project dirs for the Dir field's dropdown.
func TestFetchHubSpawnOptionsIncludesRecentDirs(t *testing.T) {
	recent := []string{"/proj/alpha", "/proj/beta"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
			return appwire.HarnessListResponse{Data: []appwire.HarnessDescriptor{{ID: "serf", Label: "serf"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodSerfProjectsRecent, func(context.Context, appwire.ProjectsRecentParams) (appwire.ProjectsRecentResponse, error) {
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
