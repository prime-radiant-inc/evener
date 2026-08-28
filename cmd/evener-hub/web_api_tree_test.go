package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
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

func TestLiveSessionGroupsBeforePastIndex(t *testing.T) {
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
	_, live, projects := web.navigationTreeInputs(context.Background())
	tree := hubBuildNavigationTree(nil, live, map[hubcore.ArchiveKey]bool{}, projects)
	if len(tree.Projects) != 1 {
		t.Fatalf("projects = %d, want 1: %+v", len(tree.Projects), tree.Projects)
	}
	if tree.Projects[0].Key != project.ID {
		t.Fatalf("project key = %q, want canonical ID %q", tree.Projects[0].Key, project.ID)
	}
	if sessions := tree.Projects[0].Current; len(sessions) != 1 || sessions[0].ID != "01L" {
		t.Fatalf("current sessions = %+v, want the live session", sessions)
	}
	lazy, ok := hubcore.BuildProjectTreeAt(nil, live, map[hubcore.ArchiveKey]bool{}, time.Now(), project.ID)
	if !ok {
		t.Fatalf("lazy project lookup did not find live project %q", project.ID)
	}
	if sessions := lazy.Current; len(sessions) != 1 || sessions[0].ID != "01L" {
		t.Fatalf("lazy current sessions = %+v, want the live session", sessions)
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

func TestAppThreadTreeEntriesPreserveRemoteLineageAndKind(t *testing.T) {
	meta, _, ok := appThreadTreeEntries(appwire.Thread{
		ID:     "child",
		Source: "remote",
		Evener: appwire.EvenerThread{
			Ref:       "remote:child",
			ParentRef: "remote:parent",
			Kind:      "subagent",
		},
	})
	if !ok {
		t.Fatal("appThreadTreeEntries rejected remote subagent")
	}
	if meta.ID != "remote:child" || meta.ParentSessionID != "remote:parent" || !meta.IsSubagent {
		t.Fatalf("remote subagent metadata = %+v", meta)
	}
}
