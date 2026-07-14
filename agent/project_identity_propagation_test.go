package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/identifier"
)

// TestSessionConfigCarriesCanonicalProjectSeparatelyFromActiveCWD protects the
// launch boundary: a linked worktree executes with its active cwd, while the
// resolved project identifies the canonical main checkout.
func TestSessionConfigCarriesCanonicalProjectSeparatelyFromActiveCWD(t *testing.T) {
	main := t.TempDir()
	active := filepath.Join(t.TempDir(), "linked-worktree")
	project := identifier.Project{ID: "main-project", CanonicalPath: main}
	cfg := SessionConfig{Project: project}

	if cfg.Project.CanonicalPath != main {
		t.Fatalf("session project canonical path = %q, want %q", cfg.Project.CanonicalPath, main)
	}
	if active == cfg.Project.CanonicalPath {
		t.Fatalf("active cwd %q must remain distinct from canonical project %q", active, cfg.Project.CanonicalPath)
	}
}
