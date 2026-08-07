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

// TestGlobWithExclusions_ReportsExcludedCountWhenFullyFiltered proves D2: a
// glob pattern that only matches paths the default dotfile/gitignore
// exclusion drops returns zero matches AND a non-zero excluded count, so a
// caller can tell "genuinely nothing matched" apart from "everything matched
// was filtered out". include_ignored=true must restore both the match and
// report zero excluded (nothing was filtered).
func TestGlobWithExclusions_ReportsExcludedCountWhenFullyFiltered(t *testing.T) {
	dir := writeScopingFixture(t)
	env := NewLocalExecutionEnvironment(dir)

	matches, excluded, err := env.GlobWithExclusions("node_modules/**/*.js", dir, false)
	if err != nil {
		t.Fatalf("GlobWithExclusions: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches (node_modules is gitignored), got: %v", matches)
	}
	if excluded == 0 {
		t.Fatal("expected a non-zero excluded count for a fully-filtered glob")
	}

	matches, excluded, err = env.GlobWithExclusions("node_modules/**/*.js", dir, true)
	if err != nil {
		t.Fatalf("GlobWithExclusions include_ignored: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected include_ignored=true to restore the match, got: %v", matches)
	}
	if excluded != 0 {
		t.Fatalf("expected excluded=0 when include_ignored is set, got %d", excluded)
	}
}

// TestSandboxedGlobWithExclusions_ReportsExcludedCountWhenFullyFiltered mirrors
// TestGlobWithExclusions_ReportsExcludedCountWhenFullyFiltered for the
// sandboxed path (sandboxFS.glob).
func TestSandboxedGlobWithExclusions_ReportsExcludedCountWhenFullyFiltered(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeScopingFixtureAt(t, worktree)

	matches, excluded, err := env.GlobWithExclusions("node_modules/**/*.js", worktree, false)
	if err != nil {
		t.Fatalf("GlobWithExclusions: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches (node_modules is gitignored), got: %v", matches)
	}
	if excluded == 0 {
		t.Fatal("expected a non-zero excluded count for a fully-filtered sandboxed glob")
	}
}

// TestGrepNative_ReportsExclusionWhenFullyFiltered proves D2's grep half: a
// pattern that only matches inside a gitignored path (node_modules) returns
// an explanatory "0 matches; N ... excluded" string rather than a bare "",
// which would be indistinguishable from a genuine no-match.
func TestGrepNative_ReportsExclusionWhenFullyFiltered(t *testing.T) {
	dir := writeScopingFixture(t)
	env := NewLocalExecutionEnvironment(dir)

	result, err := env.grepNative("module\\.exports", dir, "", false, 100, "content")
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if result == "" {
		t.Fatal("expected an explanatory message, not a bare empty string, for a fully-excluded grep")
	}
	if !strings.Contains(result, "0 matches") || !strings.Contains(result, "excluded") {
		t.Fatalf("expected an exclusion explanation, got: %q", result)
	}
}

// TestSandboxedGrepNative_ReportsExclusionWhenFullyFiltered mirrors
// TestGrepNative_ReportsExclusionWhenFullyFiltered for the sandboxed path.
func TestSandboxedGrepNative_ReportsExclusionWhenFullyFiltered(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeScopingFixtureAt(t, worktree)

	result, err := env.Grep("module\\.exports", worktree, "", false, 100, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if result == "" {
		t.Fatal("expected an explanatory message, not a bare empty string, for a fully-excluded sandboxed grep")
	}
	if !strings.Contains(result, "0 matches") || !strings.Contains(result, "excluded") {
		t.Fatalf("expected an exclusion explanation, got: %q", result)
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
