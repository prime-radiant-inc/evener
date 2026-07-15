package main

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/identifier"
)

// TestSpawnAndResumeRequestsCarryCanonicalProjectSeparatelyFromWorkingDir
// protects the hub boundary. A linked worktree remains the daemon's active
// working directory, while project identity carries the canonical checkout.
func TestSpawnAndResumeRequestsCarryCanonicalProjectSeparatelyFromWorkingDir(t *testing.T) {
	canonical := t.TempDir()
	active := filepath.Join(t.TempDir(), "linked-worktree")
	project := identifier.Project{ID: "canonical-project", CanonicalPath: canonical}
	resolved := launchconfig.Resolved{Project: project}

	spawn := hubcore.SpawnRequest{Project: project, Resolved: resolved, WorkingDir: active}
	resume := hubcore.ResumeRequest{Project: project, Resolved: resolved, WorkingDir: active}

	for name, got := range map[string]identifier.Project{"spawn": spawn.Project, "resume": resume.Project} {
		if got.CanonicalPath != canonical {
			t.Errorf("%s canonical project = %q, want %q", name, got.CanonicalPath, canonical)
		}
	}
	if spawn.WorkingDir != active || resume.WorkingDir != active {
		t.Fatalf("active working directory was replaced: spawn=%q resume=%q want=%q", spawn.WorkingDir, resume.WorkingDir, active)
	}
}
