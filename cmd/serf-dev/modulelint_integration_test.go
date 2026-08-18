//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// These run the real golangci-lint — never a stand-in. Where `make lint`
// runs, the binary exists; a machine without it skips with the reason.

func requireGolangciLint(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: runs the real golangci-lint")
	}
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skipf("golangci-lint not installed: %v", err)
	}
}

func writeFixtureModule(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture.example/lint\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestModuleLintPassesACleanModuleWithRealGolangciLint(t *testing.T) {
	requireGolangciLint(t)
	dir := writeFixtureModule(t, "package fixture\n\n// Ok exists to be lint-clean.\nfunc Ok() int { return 1 }\n")
	t.Setenv("TMPDIR", t.TempDir())
	var out, errOut strings.Builder
	code := moduleLint(lintEnv(map[string]string{"MODULES": dir}), &out, &errOut, nil, 5*time.Second)
	if code != 0 {
		t.Fatalf("moduleLint = %d, want 0\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("clean module output = %q, want two quiet lines", out.String())
	}
	if !regexp.MustCompile(`^PASS lint \(1 modules, \d+s\)$`).MatchString(lines[len(lines)-1]) {
		t.Errorf("summary = %q", lines[len(lines)-1])
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("clean integration run left scratch: %v", left)
	}
}

func TestModuleLintReportsARealFindingWithRealGolangciLint(t *testing.T) {
	requireGolangciLint(t)
	// An unused unexported function is a default-linter (unused) finding in
	// any configuration this repo has ever run.
	dir := writeFixtureModule(t, "package fixture\n\nfunc orphaned() int { return 1 }\n")
	t.Setenv("TMPDIR", t.TempDir())
	var out, errOut strings.Builder
	code := moduleLint(lintEnv(map[string]string{"MODULES": dir}), &out, &errOut, nil, 5*time.Second)
	if code != 1 {
		t.Fatalf("moduleLint = %d, want 1\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "----- "+dir+" -----") {
		t.Errorf("no replay fence for the failed module in %q", text)
	}
	if !strings.Contains(text, "orphaned") {
		t.Errorf("the real finding's identifier is missing from the replay: %q", text)
	}
	pointer := regexp.MustCompile(`(?m)^full logs: (.+)$`).FindStringSubmatch(text)
	if pointer == nil {
		t.Fatalf("no retained-log pointer in %q", text)
	}
	if _, err := os.Stat(pointer[1]); err != nil {
		t.Errorf("retained-log pointer names a missing directory: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (findings: 1/1 modules: "+dir+")" {
		t.Errorf("summary = %q", last)
	}
}
