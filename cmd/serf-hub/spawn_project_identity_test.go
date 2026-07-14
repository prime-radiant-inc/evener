package main

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/identifier"
)

func TestResolveStateDirForProjectUsesCarriedProjectWithoutResolvingWorkingDir(t *testing.T) {
	stateHome := t.TempDir()
	project := identifier.Project{ID: "carried-project", CanonicalPath: filepath.Join(stateHome, "canonical")}
	workingDir := filepath.Join(stateHome, "does-not-exist")

	got, err := resolveStateDirForProject(project, workingDir, map[string]string{
		"XDG_STATE_HOME": stateHome,
	})
	if err != nil {
		t.Fatalf("resolve carried project: %v", err)
	}
	want := filepath.Join(stateHome, "serf", "projects", project.ID)
	if got != want {
		t.Fatalf("state dir = %q, want %q", got, want)
	}
}

func TestResolveStateDirForProjectPreservesExplicitOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "state")
	project := identifier.Project{ID: "carried-project", CanonicalPath: filepath.Join(t.TempDir(), "canonical")}
	got, err := resolveStateDirForProject(project, filepath.Join(t.TempDir(), "missing"), map[string]string{
		"SERF_STATE_DIR": override,
		"XDG_STATE_HOME": filepath.Join(t.TempDir(), "unused"),
	})
	if err != nil || got != override {
		t.Fatalf("state dir = %q, err=%v, want explicit override %q", got, err, override)
	}
}
