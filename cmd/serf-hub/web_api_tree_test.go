package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
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

// TestAPISessionDetailCarriesWorkMetricsForEndedSession asserts that an ended
// (past-index-only, no live daemon) session's persisted WorkMillis and
// CumulativeUsage flow through workspaceData into apiSessionDetail's
// WorkMillis and flattened Usage fields (WS2 B2).
func TestAPISessionDetailCarriesWorkMetricsForEndedSession(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:             "01WORKMETRICS",
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
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: idx})

	detail, ok := web.apiSessionDetail("01WORKMETRICS")
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
