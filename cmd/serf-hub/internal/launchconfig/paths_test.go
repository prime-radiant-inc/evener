package launchconfig

import (
	"path/filepath"
	"testing"
)

func TestProjectID_Stable(t *testing.T) {
	a := ProjectID("/home/jesse/git/prime-radiant/serf")
	b := ProjectID("/home/jesse/git/prime-radiant/serf")
	if a != b {
		t.Errorf("ProjectID not stable: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("ProjectID length = %d, want 16", len(a))
	}
}

func TestProjectID_Differs(t *testing.T) {
	a := ProjectID("/a")
	b := ProjectID("/b")
	if a == b {
		t.Errorf("ProjectID collision for /a and /b: %q", a)
	}
}

func TestPathsFor(t *testing.T) {
	root := "/var/serf"
	cwd := "/proj"
	p := PathsFor(root, cwd)
	wantGlobal := filepath.Join(root, "launch.toml")
	wantProject := filepath.Join(cwd, ".serf", "launch.local.toml")
	wantLegacyProject := filepath.Join(root, "projects", ProjectID(cwd), "launch.toml")
	wantMeta := filepath.Join(root, "projects", ProjectID(cwd), "meta.toml")
	wantRepo := filepath.Join(cwd, ".serf", "launch.toml")
	if p.Global != wantGlobal {
		t.Errorf("Global = %q, want %q", p.Global, wantGlobal)
	}
	if p.Project != wantProject {
		t.Errorf("Project = %q, want %q", p.Project, wantProject)
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

func TestValidateRepoPath(t *testing.T) {
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

func TestValidateAbsolutePath(t *testing.T) {
	if err := ValidateAbsolutePath("/abs/path"); err != nil {
		t.Errorf("/abs/path: %v", err)
	}
	if err := ValidateAbsolutePath("rel/path"); err == nil {
		t.Errorf("rel/path: want error")
	}
}
