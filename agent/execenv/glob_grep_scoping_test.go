package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// writeScopingFixtureAt writes the same tree as writeScopingFixture into an
// existing directory, so callers that need a specific root (e.g. a sandboxed
// worktree) can reuse the fixture without a fresh t.TempDir().
func writeScopingFixtureAt(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		".gitignore":                      "node_modules/\n*.log\n",
		".git/HEAD":                       "ref: refs/heads/main\n",
		".claude/worktrees/x/scratch.txt": "scratch\n",
		"node_modules/pkg/index.js":       "module.exports = {}\n",
		"build.log":                       "build output\n",
		"src/main.go":                     "package main\n",
	}
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeScopingFixture builds a tree that exercises every unscoped-search noise
// source named in the bug report: VCS internals (.git), a worktree scratch
// dir (.claude/worktrees/x), a gitignored dependency dir (node_modules), a
// gitignored file pattern (*.log), and one ordinary tracked file (src/main.go)
// that must always survive filtering.
func writeScopingFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeScopingFixtureAt(t, dir)
	return dir
}

func TestGlob_ExcludesDotDirsAndGitignoredByDefault(t *testing.T) {
	dir := writeScopingFixture(t)
	env := NewLocalExecutionEnvironment(dir)

	got, err := env.Glob("**/*", dir)
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	for _, want := range []string{"main.go"} {
		found := false
		for _, m := range got {
			if strings.HasSuffix(m, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected tracked file %q in default (scoped) glob results, got: %v", want, got)
		}
	}

	for _, noise := range []string{".git", ".claude", "node_modules", "build.log"} {
		for _, m := range got {
			if strings.Contains(m, noise) {
				t.Fatalf("expected %q to be excluded from default glob results, but found %q in: %v", noise, m, got)
			}
		}
	}
}

func TestGlob_IncludeIgnoredRestoresExcludedPaths(t *testing.T) {
	dir := writeScopingFixture(t)
	env := NewLocalExecutionEnvironment(dir)

	got, err := env.Glob("**/*", dir, true)
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	for _, want := range []string{".git", ".claude", "node_modules", "build.log"} {
		found := false
		for _, m := range got {
			if strings.Contains(m, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected include_ignored=true to restore %q, got: %v", want, got)
		}
	}
}

func TestGrepNative_ExcludesDotDirsAndGitignoredByDefault(t *testing.T) {
	dir := writeScopingFixture(t)
	// Overwrite every fixture file's content with a common needle so a single
	// grep pattern would match all of them if scoping failed to exclude any.
	for _, rel := range []string{
		".git/HEAD",
		".claude/worktrees/x/scratch.txt",
		"node_modules/pkg/index.js",
		"build.log",
		"src/main.go",
	} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.WriteFile(full, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := NewLocalExecutionEnvironment(dir)
	result, err := env.grepNative("needle", dir, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}

	if !strings.Contains(result, "main.go") {
		t.Fatalf("expected tracked file to match, got: %q", result)
	}
	for _, noise := range []string{".git", ".claude", "node_modules", "build.log"} {
		if strings.Contains(result, noise) {
			t.Fatalf("expected %q to be excluded from grepNative results, got: %q", noise, result)
		}
	}
}

// TestSandboxedGlob_ExcludesDotDirsAndGitignoredByDefault mirrors
// TestGlob_ExcludesDotDirsAndGitignoredByDefault but routes through
// sandboxFS.glob (env.Sandbox set), which duplicates rather than shares the
// off path's exclusion wiring in securepath_browse.go — without this test a
// divergence between the two implementations would go undetected.
func TestSandboxedGlob_ExcludesDotDirsAndGitignoredByDefault(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeScopingFixtureAt(t, worktree)

	got, err := env.Glob("**/*", worktree)
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	found := false
	for _, m := range got {
		if strings.HasSuffix(m, "main.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected tracked file %q in default (scoped) sandboxed glob results, got: %v", "main.go", got)
	}

	for _, noise := range []string{".git", ".claude", "node_modules", "build.log"} {
		for _, m := range got {
			if strings.Contains(m, noise) {
				t.Fatalf("expected %q to be excluded from default sandboxed glob results, but found %q in: %v", noise, m, got)
			}
		}
	}
}

// TestSandboxedGlob_IncludeIgnoredRestoresExcludedPaths mirrors
// TestGlob_IncludeIgnoredRestoresExcludedPaths for the sandboxed path.
func TestSandboxedGlob_IncludeIgnoredRestoresExcludedPaths(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeScopingFixtureAt(t, worktree)

	got, err := env.Glob("**/*", worktree, true)
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	for _, want := range []string{".git", ".claude", "node_modules", "build.log"} {
		found := false
		for _, m := range got {
			if strings.Contains(m, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected include_ignored=true to restore %q in sandboxed glob, got: %v", want, got)
		}
	}
}

// TestSandboxedGrepNative_ExcludesDotDirsAndGitignoredByDefault mirrors
// TestGrepNative_ExcludesDotDirsAndGitignoredByDefault but routes through
// sandboxFS.grepNative (env.Sandbox set), which is the code path a real
// sandboxed session always uses for grep — see LocalExecutionEnvironment.Grep.
func TestSandboxedGrepNative_ExcludesDotDirsAndGitignoredByDefault(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeScopingFixtureAt(t, worktree)
	// Overwrite every fixture file's content with a common needle so a single
	// grep pattern would match all of them if scoping failed to exclude any.
	for _, rel := range []string{
		".git/HEAD",
		".claude/worktrees/x/scratch.txt",
		"node_modules/pkg/index.js",
		"build.log",
		"src/main.go",
	} {
		full := filepath.Join(worktree, filepath.FromSlash(rel))
		if err := os.WriteFile(full, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := env.Grep("needle", worktree, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}

	if !strings.Contains(result, "main.go") {
		t.Fatalf("expected tracked file to match, got: %q", result)
	}
	for _, noise := range []string{".git", ".claude", "node_modules", "build.log"} {
		if strings.Contains(result, noise) {
			t.Fatalf("expected %q to be excluded from sandboxed grepNative results, got: %q", noise, result)
		}
	}
}
