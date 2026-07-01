package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func s3cov_write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestS3Cov_ParseMakefileTargets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s3cov_write(t, dir, "Makefile", strings.Join([]string{
		"# a comment",
		"VAR = value",
		"build: deps",
		"\techo building",
		"test lint: build",
		"%.o: %.c",
		".PHONY: build",
		"",
	}, "\n"))
	got := parseMakefileTargets(filepath.Join(dir, "Makefile"))
	want := map[string]bool{"build": true, "test": true, "lint": true}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want keys %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected target %q in %v", g, got)
		}
	}
	// Missing file returns nil.
	if parseMakefileTargets(filepath.Join(dir, "nope")) != nil {
		t.Fatal("expected nil for missing file")
	}
}

func TestS3Cov_ParsePackageJsonScripts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s3cov_write(t, dir, "package.json", `{"scripts":{"test":"jest","build":"tsc"}}`)
	got := parsePackageJsonScripts(filepath.Join(dir, "package.json"))
	if len(got) != 2 || got[0] != "build" || got[1] != "test" {
		t.Fatalf("scripts = %v, want sorted [build test]", got)
	}
	// Missing file and invalid JSON both return nil.
	if parsePackageJsonScripts(filepath.Join(dir, "nope")) != nil {
		t.Fatal("expected nil for missing file")
	}
	s3cov_write(t, dir, "bad.json", "{not json")
	if parsePackageJsonScripts(filepath.Join(dir, "bad.json")) != nil {
		t.Fatal("expected nil for invalid JSON")
	}
}

func TestS3Cov_DetectBuildSystem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s3cov_write(t, dir, "Makefile", "build:\n\techo hi\n")
	s3cov_write(t, dir, "package.json", `{"scripts":{"start":"node ."}}`)
	s3cov_write(t, dir, "go.mod", "module example.com/foo\n\ngo 1.25\n")
	s3cov_write(t, dir, "CMakeLists.txt", "")
	s3cov_write(t, dir, "Cargo.toml", "")
	s3cov_write(t, dir, "pyproject.toml", "")
	s3cov_write(t, dir, "pytest.ini", "")
	s3cov_write(t, dir, "Dockerfile", "")
	s3cov_write(t, dir, "docker-compose.yml", "")

	got := detectBuildSystem(dir)
	for _, want := range []string{
		"Makefile targets: build",
		"package.json scripts: start",
		"Go module (go.mod): example.com/foo",
		"CMake project",
		"Rust project",
		"Python project (pyproject.toml)",
		"pytest configured",
		"Dockerfile present",
		"docker-compose.yml present",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detectBuildSystem missing %q in:\n%s", want, got)
		}
	}
}

func TestS3Cov_DetectBuildSystem_Alternates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Lowercase makefile, setup.py, docker-compose.yaml alternate branches.
	s3cov_write(t, dir, "makefile", "all:\n\techo hi\n")
	s3cov_write(t, dir, "setup.py", "")
	s3cov_write(t, dir, "docker-compose.yaml", "")
	got := detectBuildSystem(dir)
	for _, want := range []string{
		"Makefile targets: all",
		"Python project (setup.py)",
		"docker-compose.yaml present",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detectBuildSystem missing %q in:\n%s", want, got)
		}
	}
}
