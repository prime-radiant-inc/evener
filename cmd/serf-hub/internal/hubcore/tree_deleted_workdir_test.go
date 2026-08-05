package hubcore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
)

// A session whose recorded working directory no longer resolves at tree-build
// time (its worktree was deleted after the session ended) must still group
// under the identity the caller carried in for that path — and the group must
// take the resolved canonical name and working directory from any sibling
// that did resolve, never the dead directory's leaf name. The dead-worktree
// meta sorts newest-first here, so it is the one that creates the project
// accumulator: the merge must not freeze its degraded facts into the row.
func TestBuildTreeCarriedIdentityForDeletedWorkingDir(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	root := t.TempDir()
	main := filepath.Join(root, "serf")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	// Never created: the worktree is gone by the time the tree is built.
	deadLane := filepath.Join(main, ".claude", "worktrees", "webui-workspace-shell")
	mainProject, err := identifier.ResolveProject(main)
	if err != nil {
		t.Fatal(err)
	}
	old := now.Add(-20 * 24 * time.Hour) // auto-archived
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: old, UpdatedAt: old.Add(time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: deadLane}},
		{ID: "01B", CreatedAt: old, UpdatedAt: old, EnvInfo: schema.EnvironmentInfo{WorkingDir: main}},
	}
	projects := map[string]identifier.Project{
		// The carried fallback identity for the dead path: the project ID the
		// past index recorded, with no canonical path (nothing left to resolve).
		deadLane: {ID: mainProject.ID},
		main:     mainProject,
	}
	tree := BuildTreeAtWithProjects(metas, nil, map[ArchiveKey]bool{}, now, projects)
	if len(tree.Projects) != 0 || len(tree.ArchivedProjects) != 1 {
		t.Fatalf("want one archived project, got active=%d archived=%d: %+v", len(tree.Projects), len(tree.ArchivedProjects), tree.ArchivedProjects)
	}
	p := tree.ArchivedProjects[0]
	if p.Key != mainProject.ID {
		t.Errorf("Key = %q, want canonical %q", p.Key, mainProject.ID)
	}
	if p.Name != "serf" {
		t.Errorf("Name = %q, want the resolved checkout's basename %q", p.Name, "serf")
	}
	if p.WorkingDir != mainProject.CanonicalPath {
		t.Errorf("WorkingDir = %q, want canonical %q", p.WorkingDir, mainProject.CanonicalPath)
	}
	if got := p.TotalSessionCount(); got != 2 {
		t.Errorf("TotalSessionCount = %d, want 2", got)
	}
}
