package execenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs a git command in dir with a fixed identity and the local file
// protocol enabled (needed for submodule fixtures on git >= 2.38).
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	full := append([]string{"-c", "protocol.file.allow=always"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func evalSym(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return filepath.Clean(r)
}

// newLinkedWorktree builds a main repo with one commit and a linked worktree,
// returning their absolute paths.
func newLinkedWorktree(t *testing.T) (main, wt string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	runGit(t, base, "init", "-q", "main")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt = filepath.Join(base, "wt")
	runGit(t, main, "worktree", "add", "-q", wt, "-b", "feat")
	return main, wt
}

// (a) A main checkout resolves to its own root.
func TestResolveMainRepoRoot_MainRepo(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	env := NewLocalExecutionEnvironment(repo)
	if got, want := ResolveMainRepoRoot(env, repo), evalSym(t, repo); got != want {
		t.Fatalf("main repo root = %q, want %q", got, want)
	}
}

// (b) A linked worktree resolves to the MAIN root, not the worktree dir.
func TestResolveMainRepoRoot_LinkedWorktree(t *testing.T) {
	main, wt := newLinkedWorktree(t)
	env := NewLocalExecutionEnvironment(wt)
	got := ResolveMainRepoRoot(env, wt)
	if want := evalSym(t, main); got != want {
		t.Fatalf("worktree main root = %q, want main %q", got, want)
	}
	if got == evalSym(t, wt) {
		t.Fatalf("resolved to the worktree dir %q, want the main root", got)
	}
}

// (c) Launched in a repo subdirectory whose env RootDir is the subdir: the
// structural walk must use os (the env FS would reject reading ../.git).
func TestResolveMainRepoRoot_RepoSubdir(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	sub := filepath.Join(repo, "pkg", "inner")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(sub) // RootDir is the subdir
	if got, want := ResolveMainRepoRoot(env, sub), evalSym(t, repo); got != want {
		t.Fatalf("subdir main root = %q, want %q", got, want)
	}
}

// (d) --git-common-dir absolute output (linked worktree) must not be mangled by
// joining it with cwd; relative output (main checkout) is joined with cwd.
func TestResolveMainRepoRoot_CommonDirAbsoluteHandling(t *testing.T) {
	if got := mainRootCandidateFromCommonDir("/wt/abc", "/main/.git"); got != "/main" {
		t.Fatalf("absolute common: candidate = %q, want /main", got)
	}
	if got := mainRootCandidateFromCommonDir("/wt/abc", "/main/.git"); strings.Contains(got, "/wt/abc") {
		t.Fatalf("absolute common was mangled with cwd: %q", got)
	}
	if got := mainRootCandidateFromCommonDir("/repo", ".git"); got != "/repo" {
		t.Fatalf("relative common: candidate = %q, want /repo", got)
	}
}

// (e) Inside a submodule's primary checkout, resolution recovers the submodule
// working-tree root via the sanity-check fallback; two submodules of one
// superproject get different roots.
func TestResolveMainRepoRoot_Submodule(t *testing.T) {
	base := t.TempDir()
	suba := filepath.Join(base, "suba")
	subb := filepath.Join(base, "subb")
	super := filepath.Join(base, "super")
	for _, s := range []string{suba, subb, super} {
		runGit(t, base, "init", "-q", filepath.Base(s))
		runGit(t, s, "commit", "-q", "--allow-empty", "-m", "seed")
	}
	runGit(t, super, "submodule", "add", "-q", "../suba", "suba")
	runGit(t, super, "submodule", "add", "-q", "../subb", "subb")

	subaWork := filepath.Join(super, "suba")
	subbWork := filepath.Join(super, "subb")

	gotA := ResolveMainRepoRoot(NewLocalExecutionEnvironment(subaWork), subaWork)
	if want := evalSym(t, subaWork); gotA != want {
		t.Fatalf("submodule A root = %q, want %q", gotA, want)
	}
	gotB := ResolveMainRepoRoot(NewLocalExecutionEnvironment(subbWork), subbWork)
	if want := evalSym(t, subbWork); gotB != want {
		t.Fatalf("submodule B root = %q, want %q", gotB, want)
	}
	if gotA == gotB {
		t.Fatalf("two submodules resolved to the same root %q", gotA)
	}
}

// (f) The structural path resolves a linked worktree with the git binary
// entirely absent from PATH.
func TestResolveMainRepoRoot_StructuralWithoutGit(t *testing.T) {
	main, wt := newLinkedWorktree(t)
	wantMain := evalSym(t, main)
	// Hide git: PATH points at an empty dir. Setup above already used real git.
	t.Setenv("PATH", t.TempDir())
	if _, err := exec.LookPath("git"); err == nil {
		t.Skip("git still resolvable after PATH override; cannot prove no-git path")
	}
	env := NewLocalExecutionEnvironment(wt)
	if got := ResolveMainRepoRoot(env, wt); got != wantMain {
		t.Fatalf("structural (no git) main root = %q, want %q", got, wantMain)
	}
}

// (g) Not a repository resolves to "".
func TestResolveMainRepoRoot_NotARepo(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if got := ResolveMainRepoRoot(env, dir); got != "" {
		t.Fatalf("non-repo root = %q, want empty", got)
	}
}

// gitCountingShim installs a PATH-shimmed `git` that tallies each invocation and
// forwards to the real git. Returns the shim dir and a counter reader.
func gitCountingShim(t *testing.T) (string, func() int) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	shimDir := t.TempDir()
	counter := filepath.Join(shimDir, "count.log")
	script := "#!/bin/sh\nprintf 'x' >> '" + counter + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return shimDir, func() int {
		b, err := os.ReadFile(counter)
		if err != nil {
			return 0
		}
		return len(b)
	}
}

// Cache: for one cwd inside a linked worktree, GitRootOrEmpty (worktree root)
// and ResolveMainRepoRoot (main root) return different answers and each caches
// in its own slot. The counting shim proves ResolveMainRepoRoot resolves
// structurally (0 git forks) while GitRootOrEmpty forks exactly once and is
// then served from cache.
func TestResolveMainRepoRoot_SeparateCacheSlots(t *testing.T) {
	main, wt := newLinkedWorktree(t)
	wantMain := evalSym(t, main)
	wantWt := evalSym(t, wt)

	shimDir, count := gitCountingShim(t)
	t.Setenv("PATH", shimDir)

	env := NewLocalExecutionEnvironment(wt)

	r1 := ResolveMainRepoRoot(env, wt)
	if n := count(); n != 0 {
		t.Fatalf("ResolveMainRepoRoot forked git %d times, want 0 (structural)", n)
	}
	w1 := GitRootOrEmpty(env, wt)
	if n := count(); n != 1 {
		t.Fatalf("GitRootOrEmpty forked git %d times, want 1", n)
	}
	w2 := GitRootOrEmpty(env, wt)
	if n := count(); n != 1 {
		t.Fatalf("GitRootOrEmpty second call forked git (count=%d), want cached (1)", n)
	}
	r2 := ResolveMainRepoRoot(env, wt)
	if n := count(); n != 1 {
		t.Fatalf("ResolveMainRepoRoot second call forked git (count=%d), want cached/structural", n)
	}

	if r1 != wantMain || r2 != wantMain {
		t.Fatalf("main root = %q/%q, want %q", r1, r2, wantMain)
	}
	if w1 != wantWt || w2 != wantWt {
		t.Fatalf("worktree root = %q/%q, want %q", w1, w2, wantWt)
	}
	if wantMain == wantWt {
		t.Fatalf("main and worktree roots coincided (%q); cannot prove separate slots", wantMain)
	}
}

// Cache memoization: a second ResolveMainRepoRoot returns the cached value even
// after the worktree's .git pointer is removed, while a fresh env re-resolves.
func TestResolveMainRepoRoot_Memoized(t *testing.T) {
	main, wt := newLinkedWorktree(t)
	wantMain := evalSym(t, main)

	env := NewLocalExecutionEnvironment(wt)
	if got := ResolveMainRepoRoot(env, wt); got != wantMain {
		t.Fatalf("first lookup = %q, want %q", got, wantMain)
	}
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("remove worktree .git: %v", err)
	}
	if got := ResolveMainRepoRoot(env, wt); got != wantMain {
		t.Fatalf("cached lookup after .git removal = %q, want %q", got, wantMain)
	}
	if got := ResolveMainRepoRoot(NewLocalExecutionEnvironment(wt), wt); got != "" {
		t.Fatalf("fresh env after .git removal = %q, want empty", got)
	}
}
