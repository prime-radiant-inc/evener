package launchconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolve_LayersMerge(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(stateRoot, "launch.toml"), `model = "g"
skills_dirs = ["/g"]
`)
	// Trusted in-repo file.
	writeFile(t, filepath.Join(cwd, ".serf", "launch.toml"), `skills_dirs = ["sub"]`)
	repoHash, _ := CanonicalHashTOML([]byte(`skills_dirs = ["sub"]`))
	pid := ProjectID(cwd)
	writeFile(t, filepath.Join(stateRoot, "projects", pid, "meta.toml"), `schema = 1
cwd = "`+cwd+`"
[trust]
hash = "`+repoHash+`"
decision = "trusted"
`)
	writeFile(t, filepath.Join(cwd, ".serf", "launch.local.toml"), `skills_dirs = ["/p"]`)

	overrides := Layer{Model: "l", SkillsDirs: []string{"/l"}}
	got, err := Resolve(stateRoot, cwd, overrides)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.Model != "l" {
		t.Errorf("Model = %q, want l (per-launch)", got.Effective.Model)
	}
	repoExpanded := filepath.Join(cwd, "sub")
	want := []string{"/g", repoExpanded, "/p", "/l"}
	if len(got.Effective.SkillsDirs) != 4 || got.Effective.SkillsDirs[1] != repoExpanded {
		t.Errorf("SkillsDirs = %v, want %v", got.Effective.SkillsDirs, want)
	}
	if got.Repo == nil || got.Repo.Trust != TrustTrusted {
		t.Errorf("repo trust = %v, want trusted", got.Repo)
	}
}

func TestResolve_LegacyProjectLayerFallback(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	paths := PathsFor(stateRoot, cwd)
	writeFile(t, paths.LegacyProject, `model = "legacy-project"`)

	got, err := Resolve(stateRoot, cwd, Layer{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.Model != "legacy-project" {
		t.Fatalf("Model = %q, want legacy-project", got.Effective.Model)
	}
	if len(got.Diagnostics) != 1 || got.Diagnostics[0].Layer != LayerProject {
		t.Fatalf("Diagnostics = %#v, want project legacy diagnostic", got.Diagnostics)
	}
}

func TestResolve_ProjectLayerPrefersLocalFileOverLegacy(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	paths := PathsFor(stateRoot, cwd)
	writeFile(t, paths.LegacyProject, `model = "legacy-project"`)
	writeFile(t, paths.Project, `model = "local-project"`)

	got, err := Resolve(stateRoot, cwd, Layer{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.Model != "local-project" {
		t.Fatalf("Model = %q, want local-project", got.Effective.Model)
	}
	for _, d := range got.Diagnostics {
		if d.Layer == LayerProject && d.Field == "launch.local.toml" {
			t.Fatalf("unexpected legacy diagnostic when local project file exists: %#v", got.Diagnostics)
		}
	}
}

func TestResolve_UntrustedRepoSkipped(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".serf", "launch.toml"), `model = "from-repo"`)
	got, err := Resolve(stateRoot, cwd, Layer{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.Model != "" {
		t.Errorf("untrusted repo contributed Model = %q", got.Effective.Model)
	}
	if got.Repo == nil || got.Repo.Trust != TrustUntrusted {
		t.Errorf("repo state = %v, want untrusted", got.Repo)
	}
	if got.Repo.Preview == "" {
		t.Errorf("untrusted repo preview should be non-empty")
	}
}

func TestResolve_RejectedRepoSkippedSilently(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".serf", "launch.toml"), `model = "from-repo"`)
	hash, _ := CanonicalHashTOML([]byte(`model = "from-repo"`))
	pid := ProjectID(cwd)
	writeFile(t, filepath.Join(stateRoot, "projects", pid, "meta.toml"), `schema = 1
cwd = "`+cwd+`"
[trust]
hash = "`+hash+`"
decision = "rejected"
`)
	got, _ := Resolve(stateRoot, cwd, Layer{})
	if got.Repo == nil || got.Repo.Trust != TrustRejected {
		t.Errorf("repo state = %v, want rejected", got.Repo)
	}
	if got.Effective.Model != "" {
		t.Errorf("rejected repo contributed config: %v", got.Effective)
	}
}

func TestResolve_RepoPathsExpandedAndValidated(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".serf", "launch.toml"), `skills_dirs = ["../outside", "good/dir"]`)
	// Pre-trust whatever the file currently is.
	hash, _ := CanonicalHashTOML([]byte(`skills_dirs = ["../outside", "good/dir"]`))
	pid := ProjectID(cwd)
	writeFile(t, filepath.Join(stateRoot, "projects", pid, "meta.toml"), `schema = 1
cwd = "`+cwd+`"
[trust]
hash = "`+hash+`"
decision = "trusted"
`)
	got, _ := Resolve(stateRoot, cwd, Layer{})
	// "../outside" rejected; "good/dir" kept and expanded to absolute.
	if len(got.Effective.SkillsDirs) != 1 {
		t.Errorf("SkillsDirs = %v, want 1 entry (the escape rejected)", got.Effective.SkillsDirs)
	}
	hasDiag := false
	for _, d := range got.Diagnostics {
		if d.Layer == LayerRepo && d.Field == "skills_dirs" {
			hasDiag = true
		}
	}
	if !hasDiag {
		t.Errorf("expected diagnostic about ../outside, got %v", got.Diagnostics)
	}
}
