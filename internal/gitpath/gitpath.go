// Package gitpath resolves a git working directory's main repository root —
// the shared checkout that owns a linked worktree's ".git/worktrees/<id>"
// entry — using direct os and os/exec calls, with no dependency on
// agent/execenv.ExecutionEnvironment.
//
// It exists for callers that operate on the local filesystem outside an
// execenv session (hub launch config/trust resolution, CLI entry points).
// agent/execenv.ResolveMainRepoRoot implements the same algorithm for
// execenv-backed callers and wraps this package's pure structural core; see
// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1
// ("Active content root vs stable identity root").
package gitpath

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ResolveMainRepoRootLocal returns the main repository root for cwd, resolving
// linked-worktree ".git" pointer files structurally and falling back to the
// git binary. Returns "" when unresolvable (not a repository, or git is
// unavailable and the structural parse also fails).
func ResolveMainRepoRootLocal(cwd string) string {
	if root, ok := StructuralMainRoot(cwd); ok {
		return root
	}
	return gitBinaryMainRootLocal(cwd)
}

// HasGitAncestor reports whether cwd, or any ancestor directory, contains a
// ".git" entry (a directory for a main checkout, or a pointer file for a linked
// worktree/submodule). It walks up using only os.Stat and never forks git, so a
// caller can cheaply skip a `git rev-parse` subprocess for a directory that is
// clearly not inside a repository — the common case for session working dirs
// outside a repo (and every test temp dir). When it returns true a real git
// invocation is still needed to resolve the precise root; false means git would
// fail anyway.
func HasGitAncestor(cwd string) bool {
	dir := filepath.Clean(cwd)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false // reached the filesystem root without a .git entry
		}
		dir = parent
	}
}

// StructuralMainRoot resolves the main repo root using only direct os calls,
// walking up from cwd to the nearest ".git" entry. It handles main checkouts
// (".git" directory) and standard linked worktrees (".git" pointer file)
// without invoking git. It returns ok=false when there is no ".git" ancestor
// or the pointer is a non-worktree shape (e.g. a submodule), leaving those to
// the git-binary fallback.
func StructuralMainRoot(cwd string) (string, bool) {
	dir := filepath.Clean(cwd)
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return ResolveClean(dir), true // main checkout
			}
			if content, rerr := os.ReadFile(gitPath); rerr == nil {
				if root, ok := MainRootFromGitdirPointer(string(content), dir); ok {
					return ResolveClean(root), true
				}
			}
			// .git is a file but not a linked-worktree pointer (submodule, or
			// unreadable): defer to the git-binary fallback.
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false // reached the filesystem root without a .git entry
		}
		dir = parent
	}
}

// MainRootFromGitdirPointer parses a linked worktree's ".git" pointer file
// content ("gitdir: <path>") and returns the main repository root iff the
// pointer resolves to the ".git/worktrees/<id>" shape. Relative pointer paths
// are resolved against ancestorDir (the directory holding the .git file). It
// is a pure function (no filesystem access), so it is trivially fuzzable.
func MainRootFromGitdirPointer(pointerContent, ancestorDir string) (string, bool) {
	gitdir, ok := ParseGitdirPointer(pointerContent)
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(ancestorDir, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	worktreesDir := filepath.Dir(gitdir)
	if filepath.Base(worktreesDir) != "worktrees" {
		// A submodule points at ".git/modules/<sub>" (parent "modules"); anything
		// else is not a standard linked worktree. Defer to the git binary.
		return "", false
	}
	mainRoot := filepath.Dir(filepath.Dir(worktreesDir)) // worktrees -> .git -> root
	if mainRoot == "" || mainRoot == "." {
		return "", false
	}
	return mainRoot, true
}

// ParseGitdirPointer extracts the path from the first "gitdir: <path>" line of
// a git pointer file. Returns ok=false when no non-empty gitdir line is
// present.
func ParseGitdirPointer(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !ok {
			continue
		}
		if p := strings.TrimSpace(rest); p != "" {
			return p, true
		}
	}
	return "", false
}

// gitBinaryMainRootLocal is the fallback for cases the structural parse
// misses (moved worktrees, submodules). It derives a candidate main root from
// `git rev-parse --git-common-dir`, sanity-checks it, and for submodules
// falls back to `git rev-parse --show-toplevel` (the submodule's own
// working-tree root). Returns "" when git is unavailable or cwd is not in a
// repository.
func gitBinaryMainRootLocal(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	common, ok := runGitCmd(ctx, cwd, "rev-parse", "--git-common-dir")
	if !ok || common == "" {
		return ""
	}
	candidate := MainRootCandidateFromCommonDir(cwd, common)
	if candidate != "" && GitEntryResolvesToCommon(candidate, common) {
		// Coverage note: this arm needs StructuralMainRoot to have failed
		// (no cleanly-parseable ".git" pointer found walking up) while the
		// real git binary still resolves --git-common-dir correctly and
		// GitEntryResolvesToCommon confirms it — e.g. a worktree whose
		// ".git" pointer was rewritten through a symlinked alias for the
		// "worktrees" segment. Constructing that portably alongside the
		// submodule fixture below wasn't worth the fixture complexity; the
		// arm is exercised by GitEntryResolvesToCommon's own direct tests
		// (gitpath_test.go), just not through this exact call site.
		return candidate
	}

	// Submodule (or otherwise non-worktree) recovery: the common dir is
	// ".git/modules/<sub>", whose parent is not a working tree and is shared by
	// every submodule of the superproject. --show-toplevel yields the
	// submodule's own working-tree root instead.
	top, ok := runGitCmd(ctx, cwd, "rev-parse", "--show-toplevel")
	if !ok || top == "" {
		return ""
	}
	return ResolveClean(top)
}

// runGitCmd runs `git <args...>` in dir and returns its trimmed stdout. ok is
// false when git is unavailable, exits non-zero, or times out.
func runGitCmd(ctx context.Context, dir string, args ...string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// MainRootCandidateFromCommonDir turns a `git rev-parse --git-common-dir`
// result into a candidate main repo root: the parent of the common .git dir.
// The output is relative from a main checkout but absolute from a linked
// worktree, so cwd is joined only for the relative case (joining an absolute
// common with cwd would mangle it into "<cwd>/<abs>"). Symlinks in the result
// are resolved best-effort.
func MainRootCandidateFromCommonDir(cwd, common string) string {
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	common = ResolveClean(common)
	root := filepath.Dir(common)
	if root == "" || root == "." {
		return ""
	}
	return root
}

// GitEntryResolvesToCommon reports whether candidate holds a .git entry that
// resolves back to the given common .git dir. This distinguishes a genuine
// main repo root (candidate/.git IS the common dir) from a submodule's fake
// candidate (<super>/.git/modules, which has no .git entry of its own).
func GitEntryResolvesToCommon(candidate, common string) bool {
	gitPath := filepath.Join(candidate, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	common = ResolveClean(common)
	if info.IsDir() {
		return ResolveClean(gitPath) == common
	}
	// A .git pointer file (linked worktree of a worktree, or moved worktree):
	// the pointer is <common>/worktrees/<id>, two levels below the common dir.
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return false
	}
	gitdir, ok := ParseGitdirPointer(string(content))
	if !ok {
		return false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(candidate, gitdir)
	}
	return ResolveClean(filepath.Dir(filepath.Dir(gitdir))) == common
}

// ResolveClean returns the symlink-resolved, cleaned form of p, falling back
// to a plain Clean when the path cannot be resolved (e.g. it does not exist).
func ResolveClean(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}
