package main

import (
	"context"
	"html/template"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
)

// launchpadServer stubs the navigation inputs so memoTree builds a fixed tree.
func launchpadServer(t *testing.T, metas []schema.SessionMeta) *WebServer {
	t.Helper()
	orig := hubNavigationInputs
	hubNavigationInputs = func(*WebServer, context.Context) ([]schema.SessionMeta, []hubcore.LiveEntry, map[string]identifier.Project) {
		return metas, nil, map[string]identifier.Project{}
	}
	t.Cleanup(func() { hubNavigationInputs = orig })
	tmpl := template.Must(template.New("workspace_empty.html").ParseFS(templatesRoot(), "templates/partials/workspace_empty.html"))
	return &WebServer{
		workspaceEmptyTmpl: tmpl,
		treeCache:          &hubcore.TreeCache{},
	}
}

func renderEmpty(t *testing.T, s *WebServer) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/_partials/workspace/empty", nil)
	rec := httptest.NewRecorder()
	s.handleWorkspaceEmpty(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return string(body)
}

func TestWorkspaceEmptyLaunchpadRendersRecentSessions(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01OLD", OriginalPrompt: "older session", UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "01NEW", OriginalPrompt: "newer session", UpdatedAt: now.Add(-time.Hour)},
	}
	body := renderEmpty(t, launchpadServer(t, metas))
	if !strings.Contains(body, "launchpad-list") {
		t.Fatalf("expected launchpad list, got:\n%s", body)
	}
	if strings.Index(body, "newer session") > strings.Index(body, "older session") {
		t.Fatalf("rows must sort by UpdatedAt desc:\n%s", body)
	}
	if !strings.Contains(body, `href="/s/01NEW"`) {
		t.Fatalf("rows link to the session route:\n%s", body)
	}
}

func TestWorkspaceEmptyLaunchpadEscapesTitles(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "01XSS", OriginalPrompt: `<img src=x onerror=alert(1)>`, UpdatedAt: time.Now()},
	}
	body := renderEmpty(t, launchpadServer(t, metas))
	if strings.Contains(body, "<img src=x") {
		t.Fatalf("title must be HTML-escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;img src=x") {
		t.Fatalf("expected escaped title:\n%s", body)
	}
}

func TestWorkspaceEmptyZeroSessionsKeepsQuietWelcome(t *testing.T) {
	body := renderEmpty(t, launchpadServer(t, nil))
	if strings.Contains(body, "launchpad-list") {
		t.Fatalf("no sessions → no launchpad list:\n%s", body)
	}
	if !strings.Contains(body, "welcome-wordmark") {
		t.Fatalf("quiet wordmark welcome stays:\n%s", body)
	}
}

func TestWorkspaceEmptyAllArchivedSaysSearchAll(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01ARC", OriginalPrompt: "ancient session", UpdatedAt: now.Add(-30 * 24 * time.Hour)},
	}
	body := renderEmpty(t, launchpadServer(t, metas))
	if strings.Contains(body, "launchpad-list") {
		t.Fatalf("archived sessions stay off the launchpad:\n%s", body)
	}
	if !strings.Contains(body, "welcome-wordmark") {
		t.Fatalf("quiet wordmark welcome stays:\n%s", body)
	}
	if !strings.Contains(body, "Search all sessions") {
		t.Fatalf("search affordance says archived sessions exist:\n%s", body)
	}
}
