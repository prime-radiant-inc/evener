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

// (h) A bare repository has no ".git" entry of its own (structural parse
// finds nothing walking up) and `git rev-parse --show-toplevel` genuinely
// fails inside one ("this operation must be run in a work tree") — the one
// realistic way to reach gitBinaryMainRootLocal's submodule-recovery
// fallback's own failure arm (top == "" after a failed rev-parse).
func TestResolveMainRepoRootLocal_BareRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--bare", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	if got := ResolveMainRepoRootLocal(dir); got != "" {
		t.Fatalf("bare repo root = %q, want empty (show-toplevel must fail in a bare repo)", got)
	}
}

// --- ParseGitdirPointer: edge cases beyond the happy path exercised
// indirectly via newLinkedWorktree above. ---

func TestParseGitdirPointer_NoGitdirLine(t *testing.T) {
	if _, ok := ParseGitdirPointer("just some other content\n"); ok {
		t.Fatalf("ParseGitdirPointer with no gitdir line: ok=true, want false")
	}
}

func TestParseGitdirPointer_SkipsNonMatchingLinesBeforeMatch(t *testing.T) {
	got, ok := ParseGitdirPointer("not a gitdir line\ngitdir: /some/path\n")
	if !ok || got != "/some/path" {
		t.Fatalf("ParseGitdirPointer = %q, %v, want /some/path, true", got, ok)
	}
}

func TestParseGitdirPointer_EmptyGitdirValueSkipped(t *testing.T) {
	// A "gitdir:" line whose value is empty (or all whitespace) does not
	// count as a match; the parser keeps looking.
	got, ok := ParseGitdirPointer("gitdir:   \ngitdir: /real/path\n")
	if !ok || got != "/real/path" {
		t.Fatalf("ParseGitdirPointer = %q, %v, want /real/path, true", got, ok)
	}
}

// --- MainRootFromGitdirPointer: edge cases ---

func TestMainRootFromGitdirPointer_UnparseableContent(t *testing.T) {
	if _, ok := MainRootFromGitdirPointer("garbage, no gitdir prefix", "/some/dir"); ok {
		t.Fatalf("MainRootFromGitdirPointer with unparseable content: ok=true, want false")
	}
}

// TestMainRootFromGitdirPointer_MainRootCollapsesToDot covers the
// mainRoot=="" || mainRoot=="." guard: a relative gitdir pointer resolved
// against an empty ancestorDir places "worktrees" at the filesystem root of
// the (relative) path space, so climbing two levels above it collapses to
// ".".
func TestMainRootFromGitdirPointer_MainRootCollapsesToDot(t *testing.T) {
	if _, ok := MainRootFromGitdirPointer("gitdir: worktrees/id", ""); ok {
		t.Fatalf("MainRootFromGitdirPointer with a root-collapsing pointer: ok=true, want false")
	}
}

// --- MainRootCandidateFromCommonDir: the root=="" || root=="." guard ---

func TestMainRootCandidateFromCommonDir_RootCollapsesToDot(t *testing.T) {
	// An empty cwd and empty common both stay non-absolute and empty
	// through Join, so ResolveClean("") falls back to filepath.Clean("") ==
	// ".", and Dir(".") == "." — the guard's exact trigger.
	if got := MainRootCandidateFromCommonDir("", ""); got != "" {
		t.Fatalf("MainRootCandidateFromCommonDir(\"\", \"\") = %q, want empty", got)
	}
}

// --- GitEntryResolvesToCommon: direct fixture-based tests of every branch,
// since it is only reachable end-to-end via gitBinaryMainRootLocal's
// submodule-recovery path (already exercised elsewhere) which never visits
// most of its arms. ---

func TestGitEntryResolvesToCommon_NoGitEntry(t *testing.T) {
	candidate := t.TempDir()
	if GitEntryResolvesToCommon(candidate, filepath.Join(candidate, ".git")) {
		t.Fatalf("candidate with no .git entry: resolved=true, want false")
	}
}

func TestGitEntryResolvesToCommon_DirEntryMatches(t *testing.T) {
	candidate := t.TempDir()
	gitDir := filepath.Join(candidate, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !GitEntryResolvesToCommon(candidate, gitDir) {
		t.Fatalf("candidate/.git dir matching common: resolved=false, want true")
	}
}

func TestGitEntryResolvesToCommon_DirEntryMismatches(t *testing.T) {
	candidate := t.TempDir()
	gitDir := filepath.Join(candidate, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), ".git")
	if GitEntryResolvesToCommon(candidate, other) {
		t.Fatalf("candidate/.git dir NOT matching common: resolved=true, want false")
	}
}

func TestGitEntryResolvesToCommon_PointerFileUnparseable(t *testing.T) {
	candidate := t.TempDir()
	gitPath := filepath.Join(candidate, ".git")
	if err := os.WriteFile(gitPath, []byte("not a gitdir pointer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if GitEntryResolvesToCommon(candidate, filepath.Join(candidate, "elsewhere")) {
		t.Fatalf("candidate/.git file with unparseable content: resolved=true, want false")
	}
}

func TestGitEntryResolvesToCommon_PointerFileAbsoluteMatches(t *testing.T) {
	commonDir := filepath.Join(t.TempDir(), ".git")
	worktreesDir := filepath.Join(commonDir, "worktrees", "id")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	candidate := t.TempDir()
	gitPath := filepath.Join(candidate, ".git")
	if err := os.WriteFile(gitPath, []byte("gitdir: "+worktreesDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !GitEntryResolvesToCommon(candidate, commonDir) {
		t.Fatalf("candidate/.git pointer with absolute gitdir matching common: resolved=false, want true")
	}
}

func TestGitEntryResolvesToCommon_PointerFileRelativeMatches(t *testing.T) {
	candidate := t.TempDir()
	commonDir := filepath.Join(candidate, ".git")
	worktreesDir := filepath.Join(commonDir, "worktrees", "id")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(candidate, ".git")
	if err := os.RemoveAll(gitPath); err != nil {
		t.Fatal(err)
	}
	// A relative gitdir pointer is resolved against candidate (per the
	// linked-worktree convention), not against the process cwd.
	if err := os.WriteFile(gitPath, []byte("gitdir: .git/worktrees/id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !GitEntryResolvesToCommon(candidate, commonDir) {
		t.Fatalf("candidate/.git pointer with relative gitdir matching common: resolved=false, want true")
	}
}

func TestGitEntryResolvesToCommon_PointerFileUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: file permissions do not restrict reads")
	}
	candidate := t.TempDir()
	gitPath := filepath.Join(candidate, ".git")
	if err := os.WriteFile(gitPath, []byte("gitdir: /somewhere/.git/worktrees/id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gitPath, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(gitPath, 0o644) // restore so t.TempDir()'s cleanup can remove it
	if GitEntryResolvesToCommon(candidate, filepath.Join(t.TempDir(), ".git")) {
		t.Fatalf("candidate/.git file unreadable: resolved=true, want false")
	}
}

func TestGitEntryResolvesToCommon_PointerFileMismatches(t *testing.T) {
	candidate := t.TempDir()
	gitPath := filepath.Join(candidate, ".git")
	if err := os.WriteFile(gitPath, []byte("gitdir: /somewhere/else/.git/worktrees/id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if GitEntryResolvesToCommon(candidate, filepath.Join(t.TempDir(), ".git")) {
		t.Fatalf("candidate/.git pointer NOT matching common: resolved=true, want false")
	}
}

func TestHasGitAncestor(t *testing.T) {
	// A plain dir with no .git anywhere above it.
	plain := t.TempDir()
	if HasGitAncestor(plain) {
		t.Errorf("HasGitAncestor(%q) = true, want false for a non-repo dir", plain)
	}

	// A dir containing a .git directory (main checkout).
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasGitAncestor(repo) {
		t.Errorf("HasGitAncestor(%q) = false, want true when .git is a dir", repo)
	}

	// A subdirectory of that repo (walks up to find .git).
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasGitAncestor(sub) {
		t.Errorf("HasGitAncestor(%q) = false, want true for a repo subdir", sub)
	}

	// A dir containing a .git FILE (linked-worktree/submodule pointer shape).
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /somewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasGitAncestor(wt) {
		t.Errorf("HasGitAncestor(%q) = false, want true when .git is a pointer file", wt)
	}
}
