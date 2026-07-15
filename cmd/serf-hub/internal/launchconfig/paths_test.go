package launchconfig

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/identifier"
)

func checkPathsFor(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	p, err := PathsFor(root, cwd)
	if err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(cwd)
	if err != nil {
		t.Fatal(err)
	}
	wantGlobal := filepath.Join(root, "launch.toml")
	wantProject := filepath.Join(cwd, ".serf", "launch.local.toml")
	wantLegacyProject := filepath.Join(root, "projects", project.ID, "launch.toml")
	wantMeta := filepath.Join(root, "projects", project.ID, "meta.toml")
	wantRepo := filepath.Join(cwd, ".serf", "launch.toml")
	if p.Global != wantGlobal {
		t.Errorf("Global = %q, want %q", p.Global, wantGlobal)
	}
	if p.ProjectFile != wantProject || p.Project != project {
		t.Errorf("Project = %#v/%q, want %#v/%q", p.Project, p.ProjectFile, project, wantProject)
	}
	if p.LegacyProject != wantLegacyProject {
		t.Errorf("LegacyProject = %q, want %q", p.LegacyProject, wantLegacyProject)
	}
	if p.Meta != wantMeta {
		t.Errorf("Meta = %q, want %q", p.Meta, wantMeta)
	}
	if p.Repo != wantRepo {
		t.Errorf("Repo = %q, want %q", p.Repo, wantRepo)
	}
}

func TestPathsFor_ResolvesProjectIdentity(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	paths, err := PathsFor(stateRoot, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Project.ID == "" || paths.Project.CanonicalPath == "" {
		t.Fatalf("Paths.Project = %#v, want resolved project identity", paths.Project)
	}
	if filepath.Dir(filepath.Dir(paths.LegacyProject)) != filepath.Join(stateRoot, "projects") {
		t.Fatalf("LegacyProject = %q, want under state projects", paths.LegacyProject)
	}
}

func TestPathsFor_NonexistentPathReturnsError(t *testing.T) {
	if _, err := PathsFor(t.TempDir(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("PathsFor(nonexistent) returned nil error")
	}
}

func checkValidateRepoPath(t *testing.T) {
	cases := []struct {
		repo string
		path string
		want bool
	}{
		{"/repo", "sub/skills", true},
		{"/repo", "./sub/skills", true},
		{"/repo", "..plugins", true},
		{"/repo", "sub/..plugins", true},
		{"/repo", "../escape", false},
		{"/repo", "/absolute", false},
		{"/repo", "sub/../../escape", false},
	}
	for _, tc := range cases {
		err := ValidateRepoRelativePath(tc.repo, tc.path)
		got := err == nil
		if got != tc.want {
			t.Errorf("ValidateRepoRelativePath(%q, %q) = %v (err=%v), want %v", tc.repo, tc.path, got, err, tc.want)
		}
	}
}

func checkValidateAbsolutePath(t *testing.T) {
	if err := ValidateAbsolutePath("/abs/path"); err != nil {
		t.Errorf("/abs/path: %v", err)
	}
	if err := ValidateAbsolutePath("rel/path"); err == nil {
		t.Errorf("rel/path: want error")
	}
}
