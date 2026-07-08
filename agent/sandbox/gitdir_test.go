package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runGit runs a git command in dir and fails the test on error. Fixtures are
// built with the real git binary — no mocks — so the classifier is exercised
// against real .git directories and real linked-worktree/submodule pointer files.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Deterministic identity + allow local file:// submodules (recent git blocks
	// the file transport for submodules by default).
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// clean resolves symlinks + cleans, matching ClassifyWorkspace's own path
// normalization so temp-dir symlinks (e.g. /tmp) don't cause spurious mismatches.
func clean(p string) string { return resolveCleanPath(p) }

func mustContain(t *testing.T, set []string, want, label string) {
	t.Helper()
	if !slices.Contains(set, want) {
		t.Errorf("%s missing %q\n  got: %v", label, want, set)
	}
}

// TestClassifyMainCheckout: a plain `git init` repo. The classifier must root
// writes at the worktree's own .git, and mark every config + hook surface
// write-protected while leaving objects/refs/index writable.
func TestClassifyMainCheckout(t *testing.T) {
	t.Parallel()
	requireGit(t)
	root := clean(t.TempDir())
	runGit(t, root, "init", "-q")

	got, err := ClassifyWorkspace(root)
	if err != nil {
		t.Fatalf("ClassifyWorkspace: %v", err)
	}
	if got.Kind != MainCheckout {
		t.Fatalf("Kind = %v, want MainCheckout", got.Kind)
	}
	if got.WorktreeRoot != root {
		t.Errorf("WorktreeRoot = %q, want %q", got.WorktreeRoot, root)
	}
	gitDir := filepath.Join(root, ".git")
	if got.GitDir != gitDir || got.CommonDir != gitDir {
		t.Errorf("GitDir/CommonDir = %q/%q, want %q", got.GitDir, got.CommonDir, gitDir)
	}
	// Protected (write-denied) config + hook surfaces — the persistence vectors.
	mustContain(t, got.ProtectedPaths, filepath.Join(gitDir, "config"), "ProtectedPaths")
	mustContain(t, got.ProtectedPaths, filepath.Join(gitDir, "config.worktree"), "ProtectedPaths")
	mustContain(t, got.ProtectedPaths, filepath.Join(gitDir, "hooks"), "ProtectedPaths")
	// Writable metadata so commit/add/checkout still work.
	mustContain(t, got.WritablePaths, filepath.Join(gitDir, "objects"), "WritablePaths")
	mustContain(t, got.WritablePaths, filepath.Join(gitDir, "index"), "WritablePaths")
	// A main checkout's .git is inside the worktree; no external read grant.
	if len(got.ReadGrantPaths) != 0 {
		t.Errorf("ReadGrantPaths = %v, want empty for a main checkout", got.ReadGrantPaths)
	}

	// Security invariant: no protected path is also writable (a hole would let a
	// hooksPath redirect persist).
	assertNoWritableProtected(t, got)
}

// TestClassifyLinkedWorktree: `git worktree add`. The per-worktree .git is a
// pointer to <main>/.git/worktrees/<id>; the classifier must (a) recognize the
// linked shape, (b) resolve the shared common dir, (c) READ-grant the main
// .git (git must read common config from a linked worktree) while (d) still
// write-protecting main config/hooks and the per-worktree config.worktree.
func TestClassifyLinkedWorktree(t *testing.T) {
	t.Parallel()
	requireGit(t)
	main := clean(t.TempDir())
	runGit(t, main, "init", "-q")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(filepath.Dir(main), "linked-wt")
	runGit(t, main, "worktree", "add", "-q", wt)
	wt = clean(wt)

	got, err := ClassifyWorkspace(wt)
	if err != nil {
		t.Fatalf("ClassifyWorkspace: %v", err)
	}
	if got.Kind != LinkedWorktree {
		t.Fatalf("Kind = %v, want LinkedWorktree", got.Kind)
	}
	if got.WorktreeRoot != wt {
		t.Errorf("WorktreeRoot = %q, want %q", got.WorktreeRoot, wt)
	}
	commonDir := filepath.Join(main, ".git")
	if got.CommonDir != commonDir {
		t.Errorf("CommonDir = %q, want %q", got.CommonDir, commonDir)
	}
	// GitDir is the per-worktree dir under the common .git/worktrees/.
	if filepath.Dir(filepath.Dir(got.GitDir)) != commonDir || filepath.Base(filepath.Dir(got.GitDir)) != "worktrees" {
		t.Errorf("GitDir = %q, want <main>/.git/worktrees/<id>", got.GitDir)
	}
	// Read-grant the common .git so git can read common config from the worktree.
	mustContain(t, got.ReadGrantPaths, commonDir, "ReadGrantPaths")
	// Main config + hooks stay write-protected; the per-worktree config.worktree too.
	mustContain(t, got.ProtectedPaths, filepath.Join(commonDir, "config"), "ProtectedPaths")
	mustContain(t, got.ProtectedPaths, filepath.Join(commonDir, "hooks"), "ProtectedPaths")
	mustContain(t, got.ProtectedPaths, filepath.Join(got.GitDir, "config.worktree"), "ProtectedPaths")
	// Shared objects writable; per-worktree index writable.
	mustContain(t, got.WritablePaths, filepath.Join(commonDir, "objects"), "WritablePaths")
	mustContain(t, got.WritablePaths, filepath.Join(got.GitDir, "index"), "WritablePaths")

	assertNoWritableProtected(t, got)
}

// TestClassifySubmodule: a submodule's .git points to <super>/.git/modules/<name>.
// The classifier must protect the submodule's own config + hooks (the
// .git/modules/*/config surface the spec calls out).
func TestClassifySubmodule(t *testing.T) {
	t.Parallel()
	requireGit(t)
	base := clean(t.TempDir())
	upstream := filepath.Join(base, "upstream")
	super := filepath.Join(base, "super")
	runGit(t, base, "init", "-q", "upstream")
	runGit(t, upstream, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, base, "init", "-q", "super")
	runGit(t, super, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstream, "sub")

	subWT := clean(filepath.Join(super, "sub"))
	got, err := ClassifyWorkspace(subWT)
	if err != nil {
		t.Fatalf("ClassifyWorkspace: %v", err)
	}
	if got.Kind != Submodule {
		t.Fatalf("Kind = %v, want Submodule", got.Kind)
	}
	if got.WorktreeRoot != subWT {
		t.Errorf("WorktreeRoot = %q, want %q", got.WorktreeRoot, subWT)
	}
	// gitdir is under <super>/.git/modules/.
	if filepath.Base(filepath.Dir(got.GitDir)) != "modules" {
		t.Errorf("GitDir = %q, want <super>/.git/modules/<name>", got.GitDir)
	}
	mustContain(t, got.ProtectedPaths, filepath.Join(got.GitDir, "config"), "ProtectedPaths")
	mustContain(t, got.ProtectedPaths, filepath.Join(got.GitDir, "hooks"), "ProtectedPaths")

	assertNoWritableProtected(t, got)
}

// TestClassifyNestedSubmodule is the finding-#3 regression: a submodule whose
// path in the superproject contains a directory (added at "libs/foo") has its
// git dir at "<super>/.git/modules/libs/foo", whose IMMEDIATE parent is "libs",
// not "modules". Detection must recognize the ".git/modules" segment anywhere in
// the resolved git dir, not only when it is the immediate parent, or the whole
// layout is rejected and the session refuses to start in a valid repo.
func TestClassifyNestedSubmodule(t *testing.T) {
	t.Parallel()
	requireGit(t)
	base := clean(t.TempDir())
	upstream := filepath.Join(base, "upstream")
	super := filepath.Join(base, "super")
	runGit(t, base, "init", "-q", "upstream")
	runGit(t, upstream, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, base, "init", "-q", "super")
	runGit(t, super, "commit", "-q", "--allow-empty", "-m", "init")
	// A submodule at a subdirectory path → git dir ".git/modules/libs/foo".
	runGit(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstream, "libs/foo")

	subWT := clean(filepath.Join(super, "libs", "foo"))
	got, err := ClassifyWorkspace(subWT)
	if err != nil {
		t.Fatalf("ClassifyWorkspace: %v", err)
	}
	if got.Kind != Submodule {
		t.Fatalf("Kind = %v, want Submodule", got.Kind)
	}
	if got.WorktreeRoot != subWT {
		t.Errorf("WorktreeRoot = %q, want %q", got.WorktreeRoot, subWT)
	}
	// The git dir lives under a ".git/modules/" segment even though its immediate
	// parent is "libs".
	if filepath.Base(filepath.Dir(got.GitDir)) == "modules" {
		t.Errorf("test does not exercise the nested case: GitDir %q has immediate parent 'modules'", got.GitDir)
	}
	mustContain(t, got.ProtectedPaths, filepath.Join(got.GitDir, "config"), "ProtectedPaths")
	mustContain(t, got.ProtectedPaths, filepath.Join(got.GitDir, "hooks"), "ProtectedPaths")
	assertNoWritableProtected(t, got)
}

// TestClassifyMainCheckoutWithSubmoduleProtectsModuleConfig: when classifying
// the SUPERPROJECT (a main checkout that has submodules), the .git/modules/<name>/config
// surfaces must also be write-protected — otherwise a sandboxed session could
// plant a submodule hook that runs later, unsandboxed.
func TestClassifyMainCheckoutWithSubmoduleProtectsModuleConfig(t *testing.T) {
	t.Parallel()
	requireGit(t)
	base := clean(t.TempDir())
	upstream := filepath.Join(base, "upstream")
	super := clean(filepath.Join(base, "super"))
	runGit(t, base, "init", "-q", "upstream")
	runGit(t, upstream, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, base, "init", "-q", "super")
	runGit(t, super, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstream, "sub")

	got, err := ClassifyWorkspace(super)
	if err != nil {
		t.Fatalf("ClassifyWorkspace: %v", err)
	}
	if got.Kind != MainCheckout {
		t.Fatalf("Kind = %v, want MainCheckout", got.Kind)
	}
	moduleConfig := filepath.Join(super, ".git", "modules", "sub", "config")
	mustContain(t, got.ProtectedPaths, moduleConfig, "ProtectedPaths (submodule config)")
	assertNoWritableProtected(t, got)
}

// TestClassifyMainCheckoutProtectsPerWorktreeConfig is the finding-#1 regression:
// a MAIN checkout that owns a linked worktree must protect that worktree's
// per-worktree config.worktree, which can carry a core.hooksPath redirect. A
// sandboxed session must not be able to plant a hook redirect there that runs
// later, unsandboxed.
func TestClassifyMainCheckoutProtectsPerWorktreeConfig(t *testing.T) {
	t.Parallel()
	requireGit(t)
	main := clean(t.TempDir())
	runGit(t, main, "init", "-q")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := main + "-wt"
	runGit(t, main, "worktree", "add", "-q", wt)
	// Force a real per-worktree config.worktree carrying a hooksPath redirect.
	runGit(t, wt, "config", "extensions.worktreeConfig", "true")
	runGit(t, wt, "config", "--worktree", "core.hooksPath", "/tmp/evil-hooks")

	got, err := ClassifyWorkspace(main)
	if err != nil {
		t.Fatalf("ClassifyWorkspace(main): %v", err)
	}
	if got.Kind != MainCheckout {
		t.Fatalf("Kind = %v, want MainCheckout", got.Kind)
	}
	// The per-worktree config.worktree (the redirect surface) must be protected.
	var perWorktreeCfg string
	for _, p := range got.ProtectedPaths {
		if filepath.Base(p) == "config.worktree" && filepath.Base(filepath.Dir(filepath.Dir(p))) == "worktrees" {
			perWorktreeCfg = p
		}
	}
	if perWorktreeCfg == "" {
		t.Fatalf("no per-worktree config.worktree in ProtectedPaths: %v", got.ProtectedPaths)
	}
	assertNoWritableProtected(t, got)
}

// TestClassifySymlinkedGitProtectsRealConfig is the finding-#2 regression: when
// .git is a symlink, protected paths must point at the REAL git dir (not the
// symlink), so the real config/hooks are covered and cannot sit writable.
func TestClassifySymlinkedGitProtectsRealConfig(t *testing.T) {
	t.Parallel()
	requireGit(t)
	base := clean(t.TempDir())
	realGit := filepath.Join(base, "realrepo")
	runGit(t, base, "init", "-q", "realrepo")
	// A working dir whose .git is a symlink to the real repo's .git.
	wt := filepath.Join(base, "wt")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(realGit, ".git"), filepath.Join(wt, ".git")); err != nil {
		t.Fatal(err)
	}
	wt = clean(wt)

	got, err := ClassifyWorkspace(wt)
	if err != nil {
		t.Fatalf("ClassifyWorkspace: %v", err)
	}
	if got.Kind != MainCheckout {
		t.Fatalf("Kind = %v, want MainCheckout", got.Kind)
	}
	realConfig := clean(filepath.Join(realGit, ".git", "config"))
	mustContain(t, got.ProtectedPaths, realConfig, "ProtectedPaths (real config via symlink)")
	// The symlink entry itself is a redirection surface and must be protected.
	mustContain(t, got.ProtectedPaths, filepath.Join(wt, ".git"), "ProtectedPaths (.git symlink entry)")
	assertNoWritableProtected(t, got)
}

// TestClassifyLinkedWorktreeProtectsGitPointer is the finding-#3 regression: the
// .git POINTER FILE inside a linked worktree is a gitdir-redirection surface and
// must be write-protected (rewriting it swaps in an attacker-controlled git dir).
func TestClassifyLinkedWorktreeProtectsGitPointer(t *testing.T) {
	t.Parallel()
	wt := linkedWorktreeRepo(t)
	got, err := ClassifyWorkspace(wt)
	if err != nil {
		t.Fatalf("ClassifyWorkspace: %v", err)
	}
	mustContain(t, got.ProtectedPaths, filepath.Join(wt, ".git"), "ProtectedPaths (.git pointer file)")
	assertNoWritableProtected(t, got)
}

// TestClassifyNonGit: a directory outside any repository classifies as NonGit
// with no git surfaces — Resolve treats "worktree" as cwd itself.
func TestClassifyNonGit(t *testing.T) {
	t.Parallel()
	dir := clean(t.TempDir())
	got, err := ClassifyWorkspace(dir)
	if err != nil {
		t.Fatalf("ClassifyWorkspace: %v", err)
	}
	if got.Kind != NonGit {
		t.Fatalf("Kind = %v, want NonGit", got.Kind)
	}
	if got.WorktreeRoot != dir {
		t.Errorf("WorktreeRoot = %q, want %q", got.WorktreeRoot, dir)
	}
	if len(got.ProtectedPaths)+len(got.WritablePaths)+len(got.ReadGrantPaths) != 0 {
		t.Errorf("NonGit layout should carry no git surfaces: %+v", got)
	}
}

// assertNoWritableProtected is the load-bearing security invariant: no protected
// (write-denied) path may be reachable through a writable grant. If a protected
// config/hook path were nested inside a writable root, a write would slip
// through and a hooksPath redirect could persist.
func assertNoWritableProtected(t *testing.T, l GitLayout) {
	t.Helper()
	for _, prot := range l.ProtectedPaths {
		for _, w := range l.WritablePaths {
			if prot == w || isUnder(prot, w) {
				t.Errorf("protected path %q is reachable through writable root %q", prot, w)
			}
		}
	}
}

// isUnder reports whether path p is at or beneath dir.
func isUnder(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
