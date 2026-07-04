package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/rendezvous"
)

func TestArchiveDecisionsFlowIntoTree(t *testing.T) {
	// Verify that an ArchiveStore decision actually changes where a project lands
	// in the tree — i.e. the real integration path flows through BuildTreeAt.
	now := time.Unix(1_700_000_000, 0)
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	// Manually archive "alpha" even though it has a fresh session.
	if err := store.Set("project", "alpha", true, now); err != nil {
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
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
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
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	if err := store.Set("project", "beta", true, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	s := &WebServer{cfg: hubcore.WebConfig{Archive: store}}
	got := s.archiveDecisions()
	if !got[hubcore.ArchiveKey{Kind: "project", ID: "beta"}] {
		t.Fatalf("archiveDecisions() missing expected decision; got %v", got)
	}
}

func TestOrphanLiveGroupingUsesPathSlug(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "01L", WorkingDir: "/a/foo"}, SessionID: "01L", Status: "active"},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Projects) != 1 || resp.Projects[0].Key != hubcore.ProjectSlug("/a/foo") {
		t.Fatalf("orphan-live must use the path slug; got %+v", resp.Projects)
	}
}

func TestTreeResponseProjectsCarryAdditiveFields(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		// 30h old: past the 24h "current" cutoff, well inside the 14-day "recent"
		// window (see classifySession) — the plan's original "-time.Hour" fixture
		// landed in "current", not "recent" as asserted below; see Task 4 deviation notes.
		{ID: "01A", CreatedAt: now.Add(-30 * time.Hour), UpdatedAt: now.Add(-30 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/proj", GitBranch: "main"}},
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
