package launchconfig

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
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

func TestResolve_GlobalProjectInvalidPathsReportDiagnostics(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	pid := ProjectID(cwd)
	writeFile(t, filepath.Join(stateRoot, "launch.toml"), `skills_dirs = ["relative-skills", "/global-ok"]
system_prompt_mode = "file"
system_prompt_file = "relative-system.md"
trace_file = "relative-trace.out"
`)
	writeFile(t, filepath.Join(stateRoot, "projects", pid, "launch.toml"), `skills_dirs = ["project-relative-skills"]
trace_file = "project-relative-trace.out"
`)

	got, err := Resolve(stateRoot, cwd, Layer{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.SystemPromptFile != "" {
		t.Fatalf("SystemPromptFile = %q, want rejected", got.Effective.SystemPromptFile)
	}
	if got.Effective.TraceFile != "" {
		t.Fatalf("TraceFile = %q, want rejected", got.Effective.TraceFile)
	}
	if len(got.Effective.SkillsDirs) != 1 || got.Effective.SkillsDirs[0] != "/global-ok" {
		t.Fatalf("SkillsDirs = %#v, want only absolute global entry", got.Effective.SkillsDirs)
	}
	for _, want := range []Diagnostic{
		{Layer: LayerGlobal, Field: "skills_dirs"},
		{Layer: LayerGlobal, Field: "system_prompt_file"},
		{Layer: LayerGlobal, Field: "trace_file"},
		{Layer: LayerProject, Field: "skills_dirs"},
		{Layer: LayerProject, Field: "trace_file"},
	} {
		if !hasDiagnostic(got.Diagnostics, want.Layer, want.Field) {
			t.Fatalf("missing diagnostic for %s/%s in %#v", want.Layer, want.Field, got.Diagnostics)
		}
	}
}

func hasDiagnostic(diags []Diagnostic, layer LayerName, field string) bool {
	for _, d := range diags {
		if d.Layer == layer && d.Field == field {
			return true
		}
	}
	return false
}

func TestLoadProjectLayer_StatError(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	paths := PathsFor(stateRoot, cwd)
	// Make the parent component of paths.Project (<cwd>/.serf) a regular file
	// so os.Stat on paths.Project fails with ENOTDIR. This is root-proof:
	// ENOTDIR is returned regardless of uid, unlike chmod-based permission
	// bits, which root bypasses (and which would not make Stat itself fail).
	if err := os.WriteFile(filepath.Dir(paths.Project), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadProjectLayer(paths)
	if err == nil {
		t.Fatal("expected error when Stat on project path fails")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected a non-ErrNotExist Stat error, got %v", err)
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("expected ENOTDIR from the Stat-error branch, got %v", err)
	}
}

func TestLoadProjectLayer_LegacyLoadError(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	paths := PathsFor(stateRoot, cwd)
	// Create legacy path with invalid TOML so LoadLayer fails.
	if err := os.MkdirAll(filepath.Dir(paths.LegacyProject), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.LegacyProject, []byte("invalid {{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadProjectLayer(paths)
	if err == nil {
		t.Fatal("expected error when legacy project layer is malformed")
	}
}

func TestLoadRepoLayer_ReadFileError(t *testing.T) {
	cwd := t.TempDir()
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	// Make the parent component (<cwd>/.serf) a regular file so os.ReadFile on
	// repoPath fails with ENOTDIR. This is root-proof: ENOTDIR is returned
	// regardless of uid, unlike chmod 0o000, which root bypasses.
	if err := os.WriteFile(filepath.Dir(repoPath), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, diags := loadRepoLayer(cwd, "")
	var seen bool
	for _, d := range diags {
		if d.Field == ".serf/launch.toml" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("expected diagnostic for .serf/launch.toml read error, got %v", diags)
	}
}

func TestValidateAndExpandRepoLayer_InvalidPaths(t *testing.T) {
	cwd := t.TempDir()
	in := Layer{
		SkillsDirs:             []string{"../escape", "ok"},
		SystemPromptFile:       "../escape",
		SystemPromptAppendFile: "../escape",
		TraceFile:              "../escape",
		CPUProfile:             "../escape",
		ExportATIFPath:         "../escape",
	}
	got, diags := validateAndExpandRepoLayer(cwd, in)
	wantFields := []string{"skills_dirs", "system_prompt_file", "system_prompt_append_file", "trace_file", "cpu_profile", "export_atif_path"}
	for _, field := range wantFields {
		var seen bool
		for _, d := range diags {
			if d.Field == field {
				seen = true
				break
			}
		}
		if !seen {
			t.Errorf("missing diagnostic for %s", field)
		}
	}
	// The rejected "../escape" entry must be dropped and the valid "ok"
	// entry must survive, expanded to an absolute path under cwd.
	wantSkills := []string{filepath.Join(cwd, "ok")}
	if !reflect.DeepEqual(got.SkillsDirs, wantSkills) {
		t.Errorf("SkillsDirs = %v, want %v", got.SkillsDirs, wantSkills)
	}
	// Single-value fields whose only value was rejected must be emptied.
	if got.SystemPromptFile != "" {
		t.Errorf("SystemPromptFile = %q, want empty", got.SystemPromptFile)
	}
	if got.SystemPromptAppendFile != "" {
		t.Errorf("SystemPromptAppendFile = %q, want empty", got.SystemPromptAppendFile)
	}
	if got.TraceFile != "" {
		t.Errorf("TraceFile = %q, want empty", got.TraceFile)
	}
	if got.CPUProfile != "" {
		t.Errorf("CPUProfile = %q, want empty", got.CPUProfile)
	}
	if got.ExportATIFPath != "" {
		t.Errorf("ExportATIFPath = %q, want empty", got.ExportATIFPath)
	}
}
