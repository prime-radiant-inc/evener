package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/rendezvous"
)

func testProjectID(t *testing.T, path string) string {
	t.Helper()
	project, err := identifier.ResolveProject(path)
	if err != nil {
		// Fuzz/coverage callers use synthetic paths only to exercise request
		// decoding; ordinary endpoint tests use real temp directories.
		return "test-project-0000000000"
	}
	return project.ID
}

func TestArchiveDecisionsFlowIntoTree(t *testing.T) {
	// Verify that an ArchiveStore decision actually changes where a project lands
	// in the tree — i.e. the real integration path flows through BuildTreeAt.
	now := time.Unix(1_700_000_000, 0)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "alpha")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	// Manually archive the canonical project even though it has a fresh session.
	if err := store.Set("project", project.ID, true, now); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatalf("Decisions() error: %v", err)
	}
	// A fresh session for "alpha" — without the decision it would be in Projects.
	metas := []schema.SessionMeta{{
		ID:        "01ALPHA",
		UpdatedAt: now.Add(-time.Hour),
		CreatedAt: now.Add(-time.Hour),
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
	}}
	tree := hubcore.BuildTreeAt(metas, nil, decisions, now)
	// The manual-archive decision must push alpha out of Projects and into ArchivedProjects.
	for _, p := range tree.Projects {
		if p.Name == "alpha" {
			t.Fatalf("alpha should not be in Projects after manual archive; got %v", tree.Projects)
		}
	}
	found := false
	for _, p := range tree.ArchivedProjects {
		if p.Name == "alpha" {
			found = true
		}
	}
	if !found {
		t.Fatalf("alpha should be in ArchivedProjects after manual archive; got %v", tree.ArchivedProjects)
	}
}

func TestArchiveDecisionsFlowIntoTreeForSession(t *testing.T) {
	// A decision on a session ID should affect that session's tier while leaving
	// the containing project active when another non-archived session exists.
	now := time.Unix(1_700_000_001, 0)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "alpha")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{
			ID:        "01ALP1",
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now.Add(-time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
		},
		{
			ID:        "01ALP2",
			CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now.Add(-2 * time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
		},
	}
	decisions := map[hubcore.ArchiveKey]bool{
		{Kind: "session", ID: "01ALP1"}: true,
	}
	tree := hubcore.BuildTreeAt(metas, nil, decisions, now)
	if len(tree.Projects) != 1 {
		t.Fatalf("len(projects)=%d, want 1", len(tree.Projects))
	}
	if tree.Projects[0].Key != project.ID {
		t.Fatalf("unexpected project key = %q, want %q", tree.Projects[0].Key, project.ID)
	}
	if len(tree.Projects[0].Current) != 1 || tree.Projects[0].Current[0].ID != "01ALP2" {
		t.Fatalf("active session tier mismatch: current=%v", tree.Projects[0].Current)
	}
	if len(tree.Projects[0].Archived) != 1 || tree.Projects[0].Archived[0].ID != "01ALP1" {
		t.Fatalf("archived session tier mismatch: archived=%v", tree.Projects[0].Archived)
	}
	if len(tree.ArchivedProjects) != 0 {
		t.Fatalf("archived project should stay active with a live companion: archivedProjects=%v", tree.ArchivedProjects)
	}
}

func TestArchiveDecisionsHelperNilSafe(t *testing.T) {
	// A WebServer whose cfg.Archive is nil must return an empty map, never panic.
	s := &WebServer{cfg: hubcore.WebConfig{}}
	got := s.archiveDecisions()
	if got == nil {
		t.Fatal("archiveDecisions() returned nil; want empty map")
	}
	if len(got) != 0 {
		t.Fatalf("archiveDecisions() returned %v; want empty map", got)
	}
}

func TestArchiveDecisionsHelperWithStore(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "beta")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	if err := store.Set("project", project.ID, true, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	s := &WebServer{cfg: hubcore.WebConfig{Archive: store}}
	got := s.archiveDecisions()
	if !got[hubcore.ArchiveKey{Kind: "project", ID: project.ID}] {
		t.Fatalf("archiveDecisions() missing expected decision; got %v", got)
	}
}

// TestAPISessionDetailCarriesWorkMetricsForEndedSession asserts that an ended
// (past-index-only, no live daemon) session's persisted WorkMillis and
// CumulativeUsage flow through workspaceData into apiSessionDetail's
// WorkMillis and flattened Usage fields (WS2 B2).
func TestAPISessionDetailCarriesWorkMetricsForEndedSession(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-metrics-0000000000")
	sessionID := "02wMz5Txv47YP64RR3B9YJ"
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:             sessionID,
		UpdatedAt:      time.Now(),
		OriginalPrompt: "work metrics task",
		Model:          "gpt-5",
		ProfileID:      "openai",
		TurnCount:      2,
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/projects/serf"},
		WorkMillis:     7000,
		CumulativeUsage: schema.CumulativeUsage{
			InputTokens:     100,
			OutputTokens:    50,
			CacheReadTokens: 10,
			TotalTokens:     150,
		},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: idx})

	detail, ok := web.apiSessionDetail(sessionID)
	if !ok {
		t.Fatal("apiSessionDetail: session not found")
	}
	if detail.WorkMillis != 7000 {
		t.Fatalf("WorkMillis=%d, want 7000", detail.WorkMillis)
	}
	want := &hubapi.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, TotalTokens: 150}
	if detail.Usage == nil {
		t.Fatalf("Usage=nil, want %+v", want)
	}
	if *detail.Usage != *want {
		t.Fatalf("Usage=%+v, want %+v", detail.Usage, want)
	}
}

func TestOrphanLiveGroupingUsesCanonicalProjectID(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "foo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "01L", WorkingDir: project.CanonicalPath}, SessionID: "01L", Status: "active"},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Projects) != 1 || resp.Projects[0].Key != project.ID {
		t.Fatalf("orphan-live must use the canonical project ID; got %+v", resp.Projects)
	}
}

func TestNavigationTreeInputsUsesRemoteCarriedProject(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "remote-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID:          "remote-thread",
		Source:      "remote",
		CWD:         filepath.Join(project.CanonicalPath, "linked-worktree"),
		ProjectID:   project.ID,
		ProjectPath: project.CanonicalPath,
		Status:      appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
	}})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	_, live, _ := web.navigationTreeInputs(t.Context())
	if len(live) != 1 {
		t.Fatalf("live entries = %d, want 1", len(live))
	}
	if live[0].Project != project {
		t.Fatalf("remote carried project = %+v, want %+v", live[0].Project, project)
	}
}

func TestTreeResponseProjectsCarryAdditiveFields(t *testing.T) {
	now := time.Now()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		// 30h old: past the 24h "current" cutoff, well inside the 14-day "recent"
		// window (see classifySession) — the plan's original "-time.Hour" fixture
		// landed in "current", not "recent" as asserted below; see Task 4 deviation notes.
		{ID: "01A", CreatedAt: now.Add(-30 * time.Hour), UpdatedAt: now.Add(-30 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: projectDir, GitBranch: "main"}},
	}
	past := hubcore.NewPastIndex("")
	web := NewWebServer(hubcore.WebConfig{Past: past})
	web.injectMetasForTest(metas) // helper: see step 3
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	_ = resp.TestRuns // field must exist and marshal (empty slice or null both acceptable)
	if len(resp.Projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(resp.Projects))
	}
	p := resp.Projects[0]
	if p.RollupLive != 0 || p.MoreCurrent != 0 || p.Worktrees != 0 {
		t.Fatalf("additive project fields should be zero-valued here: %+v", p)
	}
	if len(p.Sessions) != 1 || p.Sessions[0].Branch != "main" || p.Sessions[0].Tier != "recent" {
		t.Fatalf("node additive fields wrong: %+v", p.Sessions)
	}
}

func TestAPITreeNodeCarriesSubagentOverflow(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{})
	got := web.apiTreeNode("project", "project-key", hubcore.TreeNode{
		ID: "parent", Kind: "session", MoreSubagents: 10,
	}, false)
	if got.MoreSubagents != 10 {
		t.Fatalf("MoreSubagents=%d, want 10", got.MoreSubagents)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"more_subagents":10`) {
		t.Fatalf("missing more_subagents wire field: %s", data)
	}
}

func TestAPITreeProjectServedFromTree(t *testing.T) {
	now := time.Now()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: project.CanonicalPath}},
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	web.injectMetasForTest(metas)
	key := project.ID
	req := httptest.NewRequest(http.MethodGet, "/api/tree/project?key="+key, nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var p hubapi.TreeProject
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Key != key || len(p.Sessions) != 1 {
		t.Fatalf("want the single project with its session, got %+v", p)
	}
}

func TestAPITreeProjectPageServesCappedAwayTierRows(t *testing.T) {
	now := time.Now()
	projectDir := filepath.Join(t.TempDir(), "paged")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := make([]schema.SessionMeta, 0, 60)
	for i := range 60 {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("01PAGE%02d", i), CreatedAt: now, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
		})
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	web.injectMetasForTest(metas)
	req := httptest.NewRequest(http.MethodGet,
		"/api/tree/project?key="+project.ID+"&tier=current&offset=50&limit=50", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Key       string            `json:"key"`
		Tier      string            `json:"tier"`
		Offset    int               `json:"offset"`
		Sessions  []hubapi.TreeNode `json:"sessions"`
		Remaining int               `json:"remaining"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Key != project.ID || page.Tier != "current" || page.Offset != 50 {
		t.Fatalf("page identity = %+v", page)
	}
	if len(page.Sessions) != 10 || page.Remaining != 0 {
		t.Fatalf("page = %d rows + %d remaining, want 10 + 0", len(page.Sessions), page.Remaining)
	}
	if page.Sessions[0].SessionID == "" || page.Sessions[0].SessionID == "01PAGE00" {
		t.Fatalf("page did not contain a capped-away session: %+v", page.Sessions[0])
	}
}

func TestAPITreeProjectPageServesCappedAwayTestRunRows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	projectDir := filepath.Join(t.TempDir(), "test-run-paged")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := make([]schema.SessionMeta, 0, 60)
	for i := range 60 {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("01TEST%02d", i), Origin: "test", CreatedAt: now, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
		})
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	web.injectMetasForTest(metas)
	treeReq := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	treeRec := httptest.NewRecorder()
	web.Handler().ServeHTTP(treeRec, treeReq)
	if treeRec.Code != http.StatusOK {
		t.Fatalf("tree status=%d body=%s", treeRec.Code, treeRec.Body.String())
	}
	var treeResp hubapi.TreeResponse
	if err := json.Unmarshal(treeRec.Body.Bytes(), &treeResp); err != nil {
		t.Fatal(err)
	}
	if len(treeResp.TestRuns) != 1 || treeResp.TestRuns[0].Key != project.ID || treeResp.TestRuns[0].MoreArchived != 10 {
		t.Fatalf("test-run tree project = %+v", treeResp.TestRuns)
	}
	req := httptest.NewRequest(http.MethodGet,
		"/api/tree/project?key="+project.ID+"&tier=archived&offset=50&limit=50", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Key      string            `json:"key"`
		Tier     string            `json:"tier"`
		Offset   int               `json:"offset"`
		Sessions []hubapi.TreeNode `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Key != project.ID || page.Tier != "archived" || page.Offset != 50 {
		t.Fatalf("page identity = %+v", page)
	}
	if len(page.Sessions) != 10 {
		t.Fatalf("page rows = %d, want 10", len(page.Sessions))
	}
}

func TestAPITreeDoesNotReprojectLiveNestedForkAsTopLevel(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "branch", ParentSessionID: "fork", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/proj"}},
		{ID: "fork", ForkLabel: "before edit", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/proj"}},
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 1}, SessionID: "branch", Status: appwire.ThreadStatusActive},
		hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 2}, SessionID: "fork", Status: appwire.ThreadStatusAwaiting},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	web.injectMetasForTest(metas)

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Projects) != 1 || len(resp.Projects[0].Sessions) != 1 || resp.Projects[0].Sessions[0].SessionID != "branch" {
		t.Fatalf("projects = %#v, want one top-level branch row", resp.Projects)
	}
	children := resp.Projects[0].Sessions[0].Children
	if len(children) != 1 || children[0].SessionID != "fork" {
		t.Fatalf("branch children = %#v, want nested fork", children)
	}
}

// TestAPITree_SummaryOnly: /api/tree?summary=1 returns just generated_at +
// attentionSummary — no tiers, no projects — so notification clients can poll
// the badge counts without downloading the whole (potentially multi-MB) tree.
func TestAPITree_SummaryOnly(t *testing.T) {
	now := time.Now()
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{SessionID: "01A", PID: 1}, SessionID: "01A", Status: "awaiting",
	})
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	web.injectMetasForTest([]schema.SessionMeta{
		{ID: "01B", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/proj"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/tree?summary=1", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, heavy := range []string{"projects", "archived_projects", "live", "needs_you", "favorites", "test_runs", "sources"} {
		if v, ok := raw[heavy]; ok && string(v) != "null" {
			t.Fatalf("summary response must not carry %q, got %s", heavy, v)
		}
	}
	if _, ok := raw["generated_at"]; !ok {
		t.Fatal("summary response missing generated_at")
	}
	var sum hubapi.AttentionSummary
	if err := json.Unmarshal(raw["attentionSummary"], &sum); err != nil {
		t.Fatal(err)
	}
	if sum.NeedsYou != 1 {
		t.Fatalf("attentionSummary.needsYou=%d, want 1 (one awaiting live session)", sum.NeedsYou)
	}
}

// TestAPITree_ArchivedProjectsAreStubs: the archive is unbounded, so the
// /api/tree snapshot ships archived projects as stubs — metadata plus a
// session_count, with sessions null. The full sessions stay available
// per-project from /api/tree/project?key= (the sidebar lazy-loads them on
// expand).
func TestAPITree_ArchivedProjectsAreStubs(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	projectDir := filepath.Join(root, "old")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(root, "index.db"))
	if err := store.Set("project", project.ID, true, now); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Archive: store})
	web.injectMetasForTest([]schema.SessionMeta{
		{ID: "01OLD1", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: project.CanonicalPath}},
		{ID: "01OLD2", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: project.CanonicalPath}},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.ArchivedProjects) != 1 {
		t.Fatalf("want 1 archived project, got %+v", resp.ArchivedProjects)
	}
	stub := resp.ArchivedProjects[0]
	if stub.Sessions != nil {
		t.Fatalf("archived stub must not ship sessions, got %d", len(stub.Sessions))
	}
	if stub.SessionCount != 2 {
		t.Fatalf("archived stub session_count=%d, want 2", stub.SessionCount)
	}
	if stub.Key != project.ID || stub.Name == "" || stub.WorkingDir != project.CanonicalPath || !stub.IsArchived {
		t.Fatalf("archived stub missing metadata: %+v", stub)
	}
	// The lazy per-project endpoint still serves the full sessions.
	req2 := httptest.NewRequest(http.MethodGet, "/api/tree/project?key="+stub.Key, nil)
	rec2 := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec2, req2)
	var full hubapi.TreeProject
	if err := json.Unmarshal(rec2.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Sessions) != 2 {
		t.Fatalf("/api/tree/project must keep full archived sessions, got %+v", full)
	}
}

func TestAPITree_ArchivedProjectStubReportsRowsBeyondSidebarCap(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	projectDir := filepath.Join(root, "old")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(root, "index.db"))
	if err := store.Set("project", project.ID, true, now); err != nil {
		t.Fatal(err)
	}
	metas := make([]schema.SessionMeta, 0, 60)
	for i := range 60 {
		updatedAt := now.Add(-15 * 24 * time.Hour).Add(-time.Duration(i) * time.Minute)
		metas = append(metas, schema.SessionMeta{
			ID:        fmt.Sprintf("01OLD%02d", i),
			CreatedAt: updatedAt,
			UpdatedAt: updatedAt,
			Name:      fmt.Sprintf("old session %d", i),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
		})
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Archive: store})
	web.injectMetasForTest(metas)
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.ArchivedProjects) != 1 {
		t.Fatalf("want 1 archived project, got %+v", resp.ArchivedProjects)
	}
	stub := resp.ArchivedProjects[0]
	if stub.SessionCount != 60 {
		t.Fatalf("archived stub session_count=%d, want authoritative total 60", stub.SessionCount)
	}
	if stub.MoreArchived != 10 {
		t.Fatalf("archived stub more_archived=%d, want sidebar overflow 10", stub.MoreArchived)
	}
}

func TestAPITree_NeedsYouCarriesAskPending(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{SessionID: "01A", PID: 1}, SessionID: "01A", Status: "awaiting", PendingAsk: true,
	})
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range resp.NeedsYou {
		if n.SessionID == "01A" && n.AskPending {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected NeedsYou[?].AskPending=true for 01A, got %+v", resp.NeedsYou)
	}
}

// The rail cannot tell a never-run session from one that ran and went quiet
// unless the fact reaches the client: /api/tree's node carries only a state,
// and "idle" is what BOTH of them report. This pins the fact onto the wire in
// the tier the sidebar actually renders, for a dormant session and for one
// with a history, so a node that quietly dropped the field fails here rather
// than in a screenshot.
func TestAPITree_CarriesDormancy(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-dormancy-0000000000")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, meta := range []schema.SessionMeta{
		{ID: "033vq9Kif27AzZgnbjr55t", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: proj}},
		{ID: "033vq9TK4UNkogAAWepGNO", UpdatedAt: now, TurnCount: 4, AcceptedInputTurns: 2,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: proj}},
	} {
		if err := schema.SaveSessionMeta(proj, meta); err != nil {
			t.Fatal(err)
		}
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: idx, Roster: hubcore.NewRosterWithEntries()})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	seen := map[string]bool{}
	for _, p := range resp.Projects {
		for _, n := range p.Sessions {
			seen[n.SessionID] = true
			got[n.SessionID] = n.Dormant
		}
	}
	if !seen["033vq9Kif27AzZgnbjr55t"] || !seen["033vq9TK4UNkogAAWepGNO"] {
		t.Fatalf("both sessions must be listed under their project, got %+v", resp.Projects)
	}
	if !got["033vq9Kif27AzZgnbjr55t"] {
		t.Errorf("01NEVERRAN Dormant = false, want true — a never-run session must say so on the wire")
	}
	if got["033vq9TK4UNkogAAWepGNO"] {
		t.Errorf("01HASRUN Dormant = true, want false — a session with turns has run")
	}
}

func TestAPISessionDetailHonorsRenamedMetaForLiveThread(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-renamed-0000000000")
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID: sessionID, UpdatedAt: time.Now(),
		Name: "my chosen name", NameSource: "user",
		Model: "gpt-5", EnvInfo: schema.EnvironmentInfo{WorkingDir: proj},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 88, Address: "127.0.0.1:4588", WorkingDir: proj, Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
	r.Refresh()
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})
	// Live daemon thread reports NO name (the rename lives only in meta).
	web.sources.Add(&scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, CWD: proj,
			Serf: appwire.SerfThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
	})
	detail, ok := web.apiSessionDetail(sessionID)
	if !ok {
		t.Fatal("apiSessionDetail: not found")
	}
	if detail.Title != "my chosen name" {
		t.Fatalf("Title=%q, want the renamed meta name (not the raw id)", detail.Title)
	}
}

// waitForNotification blocks until the client's next notification arrives (or
// t.Fatal on timeout) and returns its method name.
func waitForNotification(t *testing.T, client *appwire.Client) string {
	t.Helper()
	select {
	case got := <-client.Notifications():
		return got.Method
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a notification")
		return ""
	}
}

// assertSingleNotification waits for the client's next notification and
// asserts its method is wantMethod, then proves no SECOND notification
// follows before an ordering sentinel broadcast right after: seeing the
// sentinel next (rather than a repeat of wantMethod) rules out a
// double-broadcast without a race-prone sleep-based check.
func assertSingleNotification(t *testing.T, client *appwire.Client, server *appserver.Server, wantMethod string) {
	t.Helper()
	if got := waitForNotification(t, client); got != wantMethod {
		t.Fatalf("method=%q, want %q", got, wantMethod)
	}
	server.BroadcastAll("test/sentinel", nil)
	if got := waitForNotification(t, client); got != "test/sentinel" {
		t.Fatalf("got a second notification %q before the sentinel; want exactly one %q", got, wantMethod)
	}
}

// assertNoNotification proves nothing has reached client yet: an ordering
// sentinel is broadcast immediately, so receiving anything else first would
// mean forbiddenMethod (or something else) was already pending.
func assertNoNotification(t *testing.T, client *appwire.Client, server *appserver.Server, forbiddenMethod string) {
	t.Helper()
	server.BroadcastAll("test/sentinel", nil)
	if got := waitForNotification(t, client); got != "test/sentinel" {
		t.Fatalf("got notification %q before the sentinel; must not have broadcast %q here", got, forbiddenMethod)
	}
}

func TestRosterOnChangeNotifiesTreeChangedOnDeltaOnly(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	hubHTTP := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer hubHTTP.Close()
	client := dialHubRPC(t, hubHTTP)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: "sess1", status: "active"})
	roster.SetOnChange(func() { notifyTreeChanged(server) })

	roster.Refresh() // seed: a daemon appears in the roster — a delta
	assertSingleNotification(t, client, server, appwire.NotifySerfTreeChanged)

	roster.Refresh() // identical snapshot: no delta, must not broadcast again
	assertNoNotification(t, client, server, appwire.NotifySerfTreeChanged)
}

func TestPastIndexOnChangeNotifiesTreeChangedOnDeltaOnly(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	hubHTTP := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer hubHTTP.Close()
	client := dialHubRPC(t, hubHTTP)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	stateRoot := t.TempDir()
	proj := filepath.Join(stateRoot, "project-test-0123456789")
	meta := schema.SessionMeta{ID: "sess1", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(proj, meta); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(stateRoot, "*"))
	past.SetOnChange(func() { notifyTreeChanged(server) })

	if _, err := past.Rebuild(); err != nil { // seed: a session appears in the past index — a delta
		t.Fatal(err)
	}
	assertSingleNotification(t, client, server, appwire.NotifySerfTreeChanged)

	if _, err := past.Rebuild(); err != nil { // identical snapshot: no delta, must not broadcast again
		t.Fatal(err)
	}
	assertNoNotification(t, client, server, appwire.NotifySerfTreeChanged)
}

// TestWeb_APITreeLiveRowsCarryTierFavoriteRename covers a wire gap: the Live
// loop in handleAPITree used to call apiTreeNode directly, bypassing
// apiTreeNodeTier — the only path that stamps Tier/Branch/ClusterCount/
// Favorite/Rename. A session both live and archived would show tier=""
// (undefined) on the wire, and no live row ever carried favorite state or
// the rename affordance, regardless of the session's actual decisions.
func TestWeb_APITreeLiveRowsCarryTierFavoriteRename(t *testing.T) {
	const liveSessionID = "02wMz5Txv1C3Hut0M8GCeB"
	root := t.TempDir()
	workingDir := filepath.Join(root, "serf")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 61, Address: "127.0.0.1:4061", WorkingDir: project.CanonicalPath, Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: liveSessionID, status: appwire.ThreadStatusIdle})
	r.Refresh()
	favStore := hubcore.NewFavoriteStore(filepath.Join(root, "index.db"))
	if err := favStore.Set("session", liveSessionID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex(""), Favorite: favStore})

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Live) != 1 {
		t.Fatalf("live=%d: %+v", len(got.Live), got.Live)
	}
	node := got.Live[0]
	if node.Tier != "live" {
		t.Fatalf("live row Tier=%q, want %q: %+v", node.Tier, "live", node)
	}
	if !node.Favorite {
		t.Fatalf("live row Favorite=false, want true (session was favorited): %+v", node)
	}
	if !node.Rename {
		t.Fatalf("live row Rename=false, want true (local session): %+v", node)
	}
}

// TestWeb_APITreeOrphanLiveRowsCarryTierFavoriteRename covers the same wire
// gap surviving at a second call site: handleAPITree's orphan-live fallback
// loop projects a live session into resp.Projects whenever the PastIndex
// walk never saw it (e.g. spawned moments ago, before its meta.json is
// indexed — realistic via the archive-immediately-after-spawn window). That
// loop called apiTreeNode directly too, so such a session got a
// correctly-stamped resp.Live row (fixed above) but an unstamped
// resp.Projects row alongside it.
func TestWeb_APITreeOrphanLiveRowsCarryTierFavoriteRename(t *testing.T) {
	const liveSessionID = "02wMz5Txv1C3Hut0M8GCeB"
	root := t.TempDir()
	workingDir := filepath.Join(root, "serf")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 62, Address: "127.0.0.1:4062", WorkingDir: project.CanonicalPath, Model: "gpt-5"})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: liveSessionID, status: appwire.ThreadStatusIdle})
	r.Refresh()
	favStore := hubcore.NewFavoriteStore(filepath.Join(root, "index.db"))
	if err := favStore.Set("session", liveSessionID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	// No PastIndex entry for this session at all (nothing ever Rebuilt or
	// seeded) — this is what routes it through the orphan-live fallback loop
	// instead of the PastIndex-derived project walk.
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: hubcore.NewPastIndex(""), Favorite: favStore})

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var got hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var node *hubapi.TreeNode
	for i := range got.Projects {
		for j := range got.Projects[i].Sessions {
			if got.Projects[i].Sessions[j].SessionID == liveSessionID {
				node = &got.Projects[i].Sessions[j]
			}
		}
	}
	if node == nil {
		t.Fatalf("orphan-live session not found in any project: %+v", got.Projects)
		return // unreachable; proves the nil guard to static analysis (SA5011)
	}
	if node.Tier != "live" {
		t.Fatalf("orphan-live row Tier=%q, want %q: %+v", node.Tier, "live", *node)
	}
	if !node.Favorite {
		t.Fatalf("orphan-live row Favorite=false, want true (session was favorited): %+v", *node)
	}
	if !node.Rename {
		t.Fatalf("orphan-live row Rename=false, want true (local session): %+v", *node)
	}
}

// projectRawFromResponse decodes body as raw JSON and returns the sole
// entry of top-level key arrayKey (e.g. "projects") as a map, so a test can
// assert a field's exact wire presence/absence — an omitempty field
// unmarshaled into hubapi.TreeProject can't distinguish "false" from
// "absent," but the raw JSON can.
func projectRawFromResponse(t *testing.T, body []byte, arrayKey string) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw JSON: %v", err)
	}
	arr, _ := raw[arrayKey].([]any)
	if len(arr) != 1 {
		t.Fatalf("want 1 entry in raw %q, got %+v", arrayKey, raw[arrayKey])
	}
	entry, ok := arr[0].(map[string]any)
	if !ok {
		t.Fatalf("raw %q[0] is not an object: %+v", arrayKey, arr[0])
	}
	return entry
}

// TestWeb_APITreeProjectFavoriteStampedOnWire covers a wire gap: the favorite
// write side (POST /api/favorite) already accepts kind:"project", but
// hubapi.TreeProject had no Favorite field, so a favorited project's decision
// was unreadable from GET /api/tree.
func TestWeb_APITreeProjectFavoriteStampedOnWire(t *testing.T) {
	now := time.Now()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: projectDir}},
	}
	favStore := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favStore.Set("project", project.ID, true, now); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Favorite: favStore})
	web.injectMetasForTest(metas)

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(resp.Projects))
	}
	if !resp.Projects[0].Favorite {
		t.Fatalf("favorited project should stamp favorite=true: %+v", resp.Projects[0])
	}
	raw := projectRawFromResponse(t, rec.Body.Bytes(), "projects")
	if fav, ok := raw["favorite"]; !ok || fav != true {
		t.Fatalf("raw JSON favorite=%v (present=%v), want true", fav, ok)
	}
}

// TestWeb_APITreeProjectFavoriteOmittedWhenUnfavorited pins the other half:
// an unfavorited project must omit the favorite key entirely (omitempty),
// not send an explicit false.
func TestWeb_APITreeProjectFavoriteOmittedWhenUnfavorited(t *testing.T) {
	now := time.Now()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: projectDir}},
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	web.injectMetasForTest(metas)

	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(resp.Projects))
	}
	if resp.Projects[0].Favorite {
		t.Fatalf("unfavorited project should not be marked favorite: %+v", resp.Projects[0])
	}
	raw := projectRawFromResponse(t, rec.Body.Bytes(), "projects")
	if _, present := raw["favorite"]; present {
		t.Fatalf("unfavorited project should omit the favorite key entirely, got %+v", raw)
	}
}
