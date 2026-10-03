package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// make/building.mk stamps the GitDirty link flag by shelling out to git at
// recipe time, right after build-web has churned the tracked
// dist/PLACEHOLDER — the LDFLAGS comment there carries the full mechanism.
// These tests pin the contract from the makefile's side: the flag must read
// "" on byte-identical churn and on untracked files, and "true" on any edit
// to a tracked file — in the worktree or staged — and on a tree git itself
// cannot read, so it describes the tree the developer invoked the build on
// and fails dirty (issue #3665). The scratch repos stay hermetic against
// ambient git config so developer machines cannot decide their outcome.

// The scratch repo reproduces the real checkout's shape: the SPA build's
// one tracked sentinel, plus an ordinary source file.
const (
	placeholderRelPath = "cmd/evener-hub/frontend/dist/PLACEHOLDER"
	placeholderBody    = "run make build-web\n"
)

// dirtyCommandFromMakefile extracts the full command substitution
// make/building.mk runs for the GitDirty flag — the git predicate and the
// ""/"true" mapping — so the tests run the makefile's own logic rather than
// a copy that could drift from it. The assignment spans one line, so the
// command is everything between `GitDirty=$$(` and the `) \`
// continuation; parentheses inside belong to the command. Make's `$$`
// escaping collapses to the `$` the shell will see.
func dirtyCommandFromMakefile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "make", "building.mk"))
	if err != nil {
		t.Fatalf("read make/building.mk: %v", err)
	}
	_, rest, ok := strings.Cut(string(data), `buildinfo.GitDirty=$$(`)
	if !ok {
		t.Fatalf("make/building.mk carries no GitDirty=$$( command to test")
	}
	line, _, ok := strings.Cut(rest, "\n")
	if !ok {
		t.Fatalf("make/building.mk's GitDirty assignment is unterminated")
	}
	command, ok := strings.CutSuffix(line, `) \`)
	if !ok {
		t.Fatalf("make/building.mk's GitDirty command is not closed before its line continuation")
	}
	return strings.ReplaceAll(command, "$$", "$")
}

// gitConfigEnvironment names the git-config environment keys hermeticGitEnv
// strips before setting its own, so an ambient value cannot win by position.
var gitConfigEnvironment = []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"}

// hermeticGitEnv returns the environment the tests' git and sh subprocesses
// run with: identifier.FilteredGitEnvironment's repository-selection
// variables removed, and global and system git config pointed at nothing,
// so ambient developer config (commit.gpgsign, core.hooksPath, init
// templates) cannot decide whether the scratch repo's commits succeed —
// the same isolation hooks_test.go and app_git_head_test.go use.
func hermeticGitEnv() []string {
	base := identifier.FilteredGitEnvironment(os.Environ())
	env := make([]string, 0, len(base)+3)
outer:
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		for _, banned := range gitConfigEnvironment {
			if key == banned {
				continue outer
			}
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
}

// runGit runs git in dir with the hermetic environment.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = hermeticGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// scratchRepo builds a committed repository holding the tracked placeholder
// and one ordinary source file, the layout the real checkout has.
func scratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		runGit(t, dir, args...)
	}
	git("init", "-q", "--template=")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "test")
	placeholder := filepath.Join(dir, placeholderRelPath)
	if err := os.MkdirAll(filepath.Dir(placeholder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placeholder, []byte(placeholderBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go", placeholderRelPath)
	git("commit", "-q", "-m", "initial")
	return dir
}

// churnPlaceholder recreates the tracked placeholder byte-identically with a
// moved mtime, the state the SPA build leaves it in: same content, stale
// index stat.
func churnPlaceholder(t *testing.T, dir string) {
	t.Helper()
	placeholder := filepath.Join(dir, placeholderRelPath)
	if err := os.Remove(placeholder); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placeholder, []byte(placeholderBody), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(placeholder, future, future); err != nil {
		t.Fatal(err)
	}
}

// gitDirtyValue runs the makefile's GitDirty command in dir and returns the
// flag value it would stamp: "" when the tree reads clean, "true" when a
// tracked file reads modified. Asserting the value, not the command's exit
// status, pins the ""/"true" mapping too.
func gitDirtyValue(t *testing.T, command, dir string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = hermeticGitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run GitDirty command %q: %v\n%s", command, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitDirtyCommandIgnoresPlaceholderChurn(t *testing.T) {
	dir := scratchRepo(t)
	churnPlaceholder(t, dir)
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "" {
		t.Fatalf("GitDirty = %q on the placeholder's delete-and-recreate churn, want \"\" — "+
			"as-is, every make build stamps -dirty (issue #3665)", got)
	}
}

func TestGitDirtyCommandFlagsRealEdits(t *testing.T) {
	dir := scratchRepo(t)
	churnPlaceholder(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "true" {
		t.Fatalf("GitDirty = %q with a real edit to a tracked source file, want \"true\"", got)
	}
}

func TestGitDirtyCommandFlagsStagedEdits(t *testing.T) {
	dir := scratchRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "main.go")
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "true" {
		t.Fatalf("GitDirty = %q with a staged edit, want \"true\" — the flag must describe the tree, "+
			"not only the worktree-versus-index slice", got)
	}
}

func TestGitDirtyCommandIgnoresUntrackedFiles(t *testing.T) {
	dir := scratchRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "" {
		t.Fatalf("GitDirty = %q with only an untracked file present, want \"\"", got)
	}
}

func TestGitDirtyCommandFlagsPlaceholderEdits(t *testing.T) {
	dir := scratchRepo(t)
	if err := os.WriteFile(filepath.Join(dir, placeholderRelPath), []byte("hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "true" {
		t.Fatalf("GitDirty = %q with a hand-edited placeholder, want \"true\"", got)
	}
}

func TestGitDirtyCommandFailsDirtyOnGitError(t *testing.T) {
	dir := scratchRepo(t)
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "true" {
		t.Fatalf("GitDirty = %q when git cannot read the tree, want \"true\" — an unreadable tree must fail dirty", got)
	}
}

func TestScratchRepoIgnoresHostileGitConfig(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /bin/false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	dir := scratchRepo(t) // must not fail: the hostile signing config never reaches the scratch repo
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "" {
		t.Fatalf("GitDirty = %q on a fresh scratch repo, want \"\"", got)
	}
}
