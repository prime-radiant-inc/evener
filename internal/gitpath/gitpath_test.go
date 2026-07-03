package gitpath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit initializes a bare working repo at dir.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

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
func TestResolveMainRepoRootLocal_MainRepo(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	if got, want := ResolveMainRepoRootLocal(repo), evalSym(t, repo); got != want {
		t.Fatalf("main repo root = %q, want %q", got, want)
	}
}

// (b) A linked worktree resolves to the MAIN root, not the worktree dir.
func TestResolveMainRepoRootLocal_LinkedWorktree(t *testing.T) {
	main, wt := newLinkedWorktree(t)
	got := ResolveMainRepoRootLocal(wt)
	if want := evalSym(t, main); got != want {
		t.Fatalf("worktree main root = %q, want main %q", got, want)
	}
	if got == evalSym(t, wt) {
		t.Fatalf("resolved to the worktree dir %q, want the main root", got)
	}
}

// (c) Launched in a repo subdirectory: the structural walk must find the
// ancestor .git even though cwd is several levels below it.
func TestResolveMainRepoRootLocal_RepoSubdir(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	sub := filepath.Join(repo, "pkg", "inner")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := ResolveMainRepoRootLocal(sub), evalSym(t, repo); got != want {
		t.Fatalf("subdir main root = %q, want %q", got, want)
	}
}

// (d) --git-common-dir absolute output (linked worktree) must not be mangled by
// joining it with cwd; relative output (main checkout) is joined with cwd.
func TestMainRootCandidateFromCommonDir(t *testing.T) {
	if got := MainRootCandidateFromCommonDir("/wt/abc", "/main/.git"); got != "/main" {
		t.Fatalf("absolute common: candidate = %q, want /main", got)
	}
	if got := MainRootCandidateFromCommonDir("/wt/abc", "/main/.git"); strings.Contains(got, "/wt/abc") {
		t.Fatalf("absolute common was mangled with cwd: %q", got)
	}
	if got := MainRootCandidateFromCommonDir("/repo", ".git"); got != "/repo" {
		t.Fatalf("relative common: candidate = %q, want /repo", got)
	}
}

// (e) Inside a submodule's primary checkout, resolution recovers the submodule
// working-tree root via the sanity-check fallback; two submodules of one
// superproject get different roots.
func TestResolveMainRepoRootLocal_Submodule(t *testing.T) {
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

	gotA := ResolveMainRepoRootLocal(subaWork)
	if want := evalSym(t, subaWork); gotA != want {
		t.Fatalf("submodule A root = %q, want %q", gotA, want)
	}
	gotB := ResolveMainRepoRootLocal(subbWork)
	if want := evalSym(t, subbWork); gotB != want {
		t.Fatalf("submodule B root = %q, want %q", gotB, want)
	}
	if gotA == gotB {
		t.Fatalf("two submodules resolved to the same root %q", gotA)
	}
}

// (g) Not a repository resolves to "".
func TestResolveMainRepoRootLocal_NotARepo(t *testing.T) {
	dir := t.TempDir()
	if got := ResolveMainRepoRootLocal(dir); got != "" {
		t.Fatalf("non-repo root = %q, want empty", got)
	}
}
