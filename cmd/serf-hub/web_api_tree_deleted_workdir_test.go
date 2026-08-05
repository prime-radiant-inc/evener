package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubtest"
	"primeradiant.com/serf/identifier"
)

// Sessions that ran inside worktrees which have since been deleted must stay
// in their original project: the past index already recorded each meta under
// the canonical project's state dir, so a dead working directory is not a
// reason to mint a phantom "no-project" group named after the worktree leaf.
// Two distinct dead lanes of the SAME project make the failure mode visible
// pre-fix: two archived stubs sharing Key "no-project", which the rail then
// hydrates from a single /api/tree/project?key=no-project detail — the
// replicated archived rows this regression test guards.
func TestAPITreeDeletedWorktreeSessionsStayInOriginalProject(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "serf")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	// Neither lane ever exists on disk during the tree build: both worktrees
	// were removed after their sessions ended.
	deadA := filepath.Join(main, ".claude", "worktrees", "webui-workspace-shell")
	deadB := filepath.Join(root, "state", "worktrees", "scratchpad")
	mainProject, err := identifier.ResolveProject(main)
	if err != nil {
		t.Fatal(err)
	}

	stateRoot := t.TempDir()
	projectDir := filepath.Join(stateRoot, mainProject.ID)
	old := time.Now().Add(-20 * 24 * time.Hour) // auto-archived
	seed := func(workingDir string, age time.Duration) string {
		id := hubtest.SessionID(t)
		meta := schema.SessionMeta{
			ID:        id,
			CreatedAt: old.Add(-age),
			UpdatedAt: old.Add(-age),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: workingDir},
		}
		if err := schema.SaveSessionMeta(projectDir, meta); err != nil {
			t.Fatal(err)
		}
		return id
	}
	seed(main, 2*time.Hour)
	seed(deadA, time.Hour)
	seed(deadB, 0)

	past := hubcore.NewPastIndex(filepath.Join(stateRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, RemoteThreadCache: &hubcore.RemoteThreadCache{}})
	snapshot := web.navigationSnapshotInputs(t.Context())
	tree := hubBuildNavigationTree(snapshot.metas, snapshot.live, map[hubcore.ArchiveKey]bool{}, snapshot.projects)

	if len(tree.Projects) != 0 {
		t.Fatalf("all sessions archived, want no active projects, got %+v", tree.Projects)
	}
	if len(tree.ArchivedProjects) != 1 {
		t.Fatalf("want the three sessions in ONE archived project, got %d: %+v", len(tree.ArchivedProjects), tree.ArchivedProjects)
	}
	p := tree.ArchivedProjects[0]
	if p.Key != mainProject.ID {
		t.Errorf("Key = %q, want canonical %q", p.Key, mainProject.ID)
	}
	if p.Name != "serf" {
		t.Errorf("Name = %q, want the original project's name %q, not a worktree leaf", p.Name, "serf")
	}
	if got := p.TotalSessionCount(); got != 3 {
		t.Errorf("TotalSessionCount = %d, want 3", got)
	}
}
