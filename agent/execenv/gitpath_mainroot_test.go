package execenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeExecEnv is a minimal, fully scripted ExecutionEnvironment: every
// ExecCommand call is answered by a caller-supplied function, and every
// filesystem/query method is an inert stub. It exists to reach gitpath.go's
// branches that need a specific ExecCommand response (empty stdout, a
// non-zero exit, or a git-common-dir that doesn't match a real repo's
// structural shape) that real git cannot be coaxed into producing on
// demand — the ExecutionEnvironment interface is exactly this package's own
// seam for that (real git is exercised everywhere else in this file via
// NewLocalExecutionEnvironment).
type fakeExecEnv struct {
	workDir string
	exec    func(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
}

func (f *fakeExecEnv) Initialize() error        { return nil }
func (f *fakeExecEnv) Cleanup()                 {}
func (f *fakeExecEnv) WorkingDirectory() string { return f.workDir }
func (f *fakeExecEnv) Platform() string         { return "test" }
func (f *fakeExecEnv) OSVersion() string        { return "test" }

func (f *fakeExecEnv) ReadFile(string, *int, *int) (string, error)           { return "", nil }
func (f *fakeExecEnv) WriteFile(string, string) (string, error)              { return "", nil }
func (f *fakeExecEnv) EditFile(string, string, string, bool) (string, error) { return "", nil }
func (f *fakeExecEnv) FileExists(string) bool                                { return false }
func (f *fakeExecEnv) Glob(string, string) ([]string, error)                 { return nil, nil }
func (f *fakeExecEnv) Grep(string, string, string, bool, int, string) (string, error) {
	return "", nil
}
func (f *fakeExecEnv) ListDirectory(string, int) ([]DirEntry, error) { return nil, nil }

func (f *fakeExecEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	return f.exec(ctx, command, timeoutMS, workingDir, envVars)
}

var _ ExecutionEnvironment = (*fakeExecEnv)(nil)

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

// gitCountingShim installs a PATH-shimmed `git` that tallies each invocation
// and answers `rev-parse --show-toplevel` directly with toplevel, rather than
// forwarding to a second, real git process. GitRootOrEmpty races this shim
// against its own hardcoded 2s exec timeout (gitRootUncached); under heavy
// concurrent load from sibling packages' tests (this repo's agent package
// alone has 2000+ t.Parallel subtests) a shim that forks a real git on top of
// itself can occasionally lose that race purely on scheduling, independent of
// whether the caching logic under test is correct — confirmed by direct
// reproduction (a run that took just over 2s and recorded 0 forks instead of
// 1). Keeping the shim to a single, trivial process removes that extra hop
// without weakening the count assertions below (still exactly-once, still
// cache-verified) or touching the production timeout. Returns the shim dir
// and a counter reader.
func gitCountingShim(t *testing.T, toplevel string) (string, func() int) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	shimDir := t.TempDir()
	counter := filepath.Join(shimDir, "count.log")
	script := "#!/bin/sh\nprintf 'x' >> '" + counter + "'\nprintf '%s\\n' '" + toplevel + "'\n"
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
	// This test asserts fork COUNTS (structural caching behavior), not
	// latency, so its correctness must not depend on machine load. Widen the
	// production 2s git-exec deadline for this test only: under heavy
	// parallel load from sibling packages' tests, even forking the trivial
	// counting shim below can occasionally exceed 2s on a contended machine,
	// which would starve the shim before it wrote its counter byte and make
	// this test flake for a reason unrelated to the caching logic it checks.
	orig := gitExecTimeout
	gitExecTimeout = 30 * time.Second
	t.Cleanup(func() { gitExecTimeout = orig })

	main, wt := newLinkedWorktree(t)
	wantMain := evalSym(t, main)
	wantWt := evalSym(t, wt)

	shimDir, count := gitCountingShim(t, wantWt)
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

// --- Non-*LocalExecutionEnvironment callers: GitRootOrEmpty and
// ResolveMainRepoRoot both special-case *LocalExecutionEnvironment for
// caching and fall straight through to the uncached resolver for any other
// ExecutionEnvironment implementation. ---

func TestGitRootOrEmpty_NonLocalEnv(t *testing.T) {
	repo := t.TempDir()
	env := &fakeExecEnv{
		workDir: repo,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			if strings.Contains(command, "--show-toplevel") {
				return ExecResult{Stdout: repo, ExitCode: 0}, nil
			}
			return ExecResult{ExitCode: 1}, nil
		},
	}
	if got, want := GitRootOrEmpty(env, repo), evalSym(t, repo); got != want {
		t.Fatalf("GitRootOrEmpty (non-local env) = %q, want %q", got, want)
	}
}

func TestResolveMainRepoRoot_NonLocalEnv(t *testing.T) {
	// A plain (non-repo) dir: structuralMainRoot fails immediately, so
	// ResolveMainRepoRoot must fall through to mainRepoRootUncached and
	// invoke the fake env's ExecCommand rather than ever touching a cache.
	dir := t.TempDir()
	called := false
	env := &fakeExecEnv{
		workDir: dir,
		exec: func(_ context.Context, _ string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			called = true
			return ExecResult{ExitCode: 1}, nil
		},
	}
	if got := ResolveMainRepoRoot(env, dir); got != "" {
		t.Fatalf("ResolveMainRepoRoot (non-local env) = %q, want empty", got)
	}
	if !called {
		t.Fatalf("ResolveMainRepoRoot (non-local env) never called ExecCommand")
	}
}

// --- gitRootUncached: response-shape edge cases only reachable with a
// scripted ExecCommand response (real git never emits exit 0 with empty
// stdout, or a toplevel that isn't a prefix of cwd). ---

func TestGitRootOrEmpty_EmptyStdout(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{
		workDir: dir,
		exec: func(_ context.Context, _ string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			return ExecResult{Stdout: "   \n", ExitCode: 0}, nil
		},
	}
	if got := GitRootOrEmpty(env, dir); got != "" {
		t.Fatalf("GitRootOrEmpty with blank stdout = %q, want empty", got)
	}
}

func TestGitRootOrEmpty_ReportedRootNotPrefixOfCwd(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	env := &fakeExecEnv{
		workDir: dir,
		exec: func(_ context.Context, _ string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			// A root that shares no path relationship with cwd at all — the
			// sanity check must reject it rather than trust git blindly.
			return ExecResult{Stdout: elsewhere, ExitCode: 0}, nil
		},
	}
	if got := GitRootOrEmpty(env, dir); got != "" {
		t.Fatalf("GitRootOrEmpty with a root that isn't a prefix of cwd = %q, want empty", got)
	}
}

// --- gitBinaryMainRoot: response-shape edge cases ---

func TestResolveMainRepoRoot_CommonDirEmptyStdout(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{
		workDir: dir,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			if strings.Contains(command, "--git-common-dir") {
				return ExecResult{Stdout: "  \n", ExitCode: 0}, nil
			}
			return ExecResult{ExitCode: 1}, nil
		},
	}
	if got := ResolveMainRepoRoot(env, dir); got != "" {
		t.Fatalf("ResolveMainRepoRoot with blank --git-common-dir stdout = %q, want empty", got)
	}
}

// TestResolveMainRepoRoot_PrimaryGitBinaryCandidateSucceeds covers
// gitBinaryMainRoot's primary success arm (candidate resolves without
// falling to the --show-toplevel submodule recovery): a real ".git"
// directory at <candidate>/.git, reported as the common dir by a scripted
// --git-common-dir, with cwd deliberately outside candidate's ancestry so
// structuralMainRoot's plain directory walk never finds it and defers to
// the git binary — exactly the "moved worktree" shape gitBinaryMainRoot's
// doc comment describes.
func TestResolveMainRepoRoot_PrimaryGitBinaryCandidateSucceeds(t *testing.T) {
	candidate := t.TempDir()
	if err := os.MkdirAll(filepath.Join(candidate, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir() // unrelated to candidate; structural walk finds nothing
	common := filepath.Join(candidate, ".git")

	env := &fakeExecEnv{
		workDir: cwd,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			if strings.Contains(command, "--git-common-dir") {
				return ExecResult{Stdout: common, ExitCode: 0}, nil
			}
			t.Fatalf("unexpected ExecCommand call: %q (want only --git-common-dir)", command)
			return ExecResult{ExitCode: 1}, nil
		},
	}
	want := evalSym(t, candidate)
	if got := ResolveMainRepoRoot(env, cwd); got != want {
		t.Fatalf("ResolveMainRepoRoot (primary git-binary candidate) = %q, want %q", got, want)
	}
}

func TestResolveMainRepoRoot_ShowToplevelFails(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{
		workDir: dir,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			switch {
			case strings.Contains(command, "--git-common-dir"):
				// A common dir with no corresponding real .git entry anywhere
				// forces the fallback to --show-toplevel.
				return ExecResult{Stdout: filepath.Join(dir, "nonexistent", ".git"), ExitCode: 0}, nil
			case strings.Contains(command, "--show-toplevel"):
				return ExecResult{ExitCode: 128}, nil
			}
			return ExecResult{ExitCode: 1}, nil
		},
	}
	if got := ResolveMainRepoRoot(env, dir); got != "" {
		t.Fatalf("ResolveMainRepoRoot with a failing --show-toplevel fallback = %q, want empty", got)
	}
}

func TestResolveMainRepoRoot_ShowToplevelEmptyStdout(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{
		workDir: dir,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			switch {
			case strings.Contains(command, "--git-common-dir"):
				return ExecResult{Stdout: filepath.Join(dir, "nonexistent", ".git"), ExitCode: 0}, nil
			case strings.Contains(command, "--show-toplevel"):
				return ExecResult{Stdout: "  \n", ExitCode: 0}, nil
			}
			return ExecResult{ExitCode: 1}, nil
		},
	}
	if got := ResolveMainRepoRoot(env, dir); got != "" {
		t.Fatalf("ResolveMainRepoRoot with blank --show-toplevel stdout = %q, want empty", got)
	}
}

// --- DirsFromRootToCwd ---

func TestDirsFromRootToCwd_RelError(t *testing.T) {
	// filepath.Rel refuses to relate a relative path to an absolute one; the
	// function must fail safe to []{cwd} rather than propagate the error.
	got := DirsFromRootToCwd("relative/root", "/absolute/cwd")
	want := []string{"/absolute/cwd"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("DirsFromRootToCwd with an unrelatable root/cwd pair = %v, want %v", got, want)
	}
}

func TestDirsFromRootToCwd_Basic(t *testing.T) {
	got := DirsFromRootToCwd("/a/b", "/a/b/c/d")
	want := []string{"/a/b", "/a/b/c", "/a/b/c/d"}
	if len(got) != len(want) {
		t.Fatalf("DirsFromRootToCwd = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DirsFromRootToCwd[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestDirsFromRootToCwd_CwdOutsideRoot(t *testing.T) {
	got := DirsFromRootToCwd("/a/b", "/x/y")
	want := []string{"/x/y"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("DirsFromRootToCwd with cwd outside root = %v, want %v", got, want)
	}
}

func TestDirsFromRootToCwd_RootEqualsCwd(t *testing.T) {
	got := DirsFromRootToCwd("/a/b", "/a/b")
	want := []string{"/a/b"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("DirsFromRootToCwd with root==cwd = %v, want %v", got, want)
	}
}
