package execenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GitRootOrEmpty returns the absolute path of the git working-tree root
// containing cwd, or "" if cwd is not inside a git repository (or git is
// unavailable). It runs `git rev-parse --show-toplevel` in env with a short
// timeout, resolves symlinks, and sanity-checks that the reported root is a
// prefix of cwd.
func GitRootOrEmpty(env ExecutionEnvironment, cwd string) string {
	// Memoize per environment: a session resolves the git root several times at
	// init, all on the same env and cwd, so fork `git rev-parse` once.
	if local, ok := env.(*LocalExecutionEnvironment); ok && local.gitRoots != nil {
		return local.gitRoots.lookup(cwd, func() string { return gitRootUncached(env, cwd) })
	}
	return gitRootUncached(env, cwd)
}

func gitRootUncached(env ExecutionEnvironment, cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := env.ExecCommand(ctx, "git rev-parse --show-toplevel", 2_000, cwd, nil)
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	root := strings.TrimSpace(res.Stdout)
	if root == "" {
		return ""
	}
	// Best-effort sanity check: ensure the returned root is a prefix of cwd.
	// Resolve symlinks to handle macOS /var -> /private/var and similar.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	root = filepath.Clean(root)
	cwd = filepath.Clean(cwd)
	if root != cwd && !strings.HasPrefix(cwd, root+string(filepath.Separator)) {
		return ""
	}
	return root
}

// ResolveMainRepoRoot returns the main repository root for cwd, resolving
// linked-worktree .git pointer files structurally and falling back to the
// git binary. Returns "" when unresolvable.
//
// Unlike GitRootOrEmpty (which reports the active working-tree root), this
// resolves a linked worktree back to the shared main checkout, so the two
// functions can legitimately return different roots for the same cwd. It keeps
// a separate per-environment cache slot for that reason.
func ResolveMainRepoRoot(env ExecutionEnvironment, cwd string) string {
	if local, ok := env.(*LocalExecutionEnvironment); ok && local.mainRoots != nil {
		return local.mainRoots.lookup(cwd, func() string { return mainRepoRootUncached(env, cwd) })
	}
	return mainRepoRootUncached(env, cwd)
}

func mainRepoRootUncached(env ExecutionEnvironment, cwd string) string {
	if root, ok := structuralMainRoot(cwd); ok {
		return root
	}
	return gitBinaryMainRoot(env, cwd)
}

// structuralMainRoot resolves the main repo root using only direct os calls,
// walking up from cwd to the nearest .git entry. It handles main checkouts
// (.git directory) and standard linked worktrees (.git pointer file) without
// invoking git. It returns ok=false when there is no .git ancestor or the
// pointer is a non-worktree shape (e.g. a submodule), leaving those to the
// git-binary fallback.
//
// The walk uses os directly, not the env's confined file API: when serf is
// launched in a repo subdirectory, .git lives above the env RootDir, where the
// env would reject the read and silently break resolution.
func structuralMainRoot(cwd string) (string, bool) {
	dir := filepath.Clean(cwd)
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return resolveClean(dir), true // main checkout
			}
			if content, rerr := os.ReadFile(gitPath); rerr == nil {
				if root, ok := mainRootFromGitdirPointer(string(content), dir); ok {
					return resolveClean(root), true
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

// mainRootFromGitdirPointer parses a linked worktree's ".git" pointer file
// content ("gitdir: <path>") and returns the main repository root iff the
// pointer resolves to the ".git/worktrees/<id>" shape. Relative pointer paths
// are resolved against ancestorDir (the directory holding the .git file). It is
// a pure function (no filesystem access) so it is trivially fuzzable and
// extractable into internal/gitpath.
func mainRootFromGitdirPointer(pointerContent, ancestorDir string) (string, bool) {
	gitdir, ok := parseGitdirPointer(pointerContent)
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

// parseGitdirPointer extracts the path from the first "gitdir: <path>" line of a
// git pointer file. Returns ok=false when no non-empty gitdir line is present.
func parseGitdirPointer(content string) (string, bool) {
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

// gitBinaryMainRoot is the fallback for cases the structural parse misses (moved
// worktrees, submodules). It derives a candidate main root from
// `git rev-parse --git-common-dir`, sanity-checks it, and for submodules falls
// back to `git rev-parse --show-toplevel` (the submodule's own working-tree
// root). Returns "" when git is unavailable or cwd is not in a repository.
func gitBinaryMainRoot(env ExecutionEnvironment, cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := env.ExecCommand(ctx, "git rev-parse --git-common-dir", 2_000, cwd, nil)
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	common := strings.TrimSpace(res.Stdout)
	if common == "" {
		return ""
	}
	candidate := mainRootCandidateFromCommonDir(cwd, common)
	if candidate != "" && gitEntryResolvesToCommon(candidate, common) {
		return candidate
	}

	// Submodule (or otherwise non-worktree) recovery: the common dir is
	// ".git/modules/<sub>", whose parent is not a working tree and is shared by
	// every submodule of the superproject. --show-toplevel yields the
	// submodule's own working-tree root instead.
	res2, err := env.ExecCommand(ctx, "git rev-parse --show-toplevel", 2_000, cwd, nil)
	if err != nil || res2.ExitCode != 0 {
		return ""
	}
	top := strings.TrimSpace(res2.Stdout)
	if top == "" {
		return ""
	}
	return resolveClean(top)
}

// mainRootCandidateFromCommonDir turns a `git rev-parse --git-common-dir` result
// into a candidate main repo root: the parent of the common .git dir. The output
// is relative from a main checkout but absolute from a linked worktree, so cwd
// is joined only for the relative case (joining an absolute common with cwd
// would mangle it into "<cwd>/<abs>"). Symlinks in the result are resolved
// best-effort.
func mainRootCandidateFromCommonDir(cwd, common string) string {
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	common = resolveClean(common)
	root := filepath.Dir(common)
	if root == "" || root == "." {
		return ""
	}
	return root
}

// gitEntryResolvesToCommon reports whether candidate holds a .git entry that
// resolves back to the given common .git dir. This distinguishes a genuine main
// repo root (candidate/.git IS the common dir) from a submodule's fake candidate
// (<super>/.git/modules, which has no .git entry of its own).
func gitEntryResolvesToCommon(candidate, common string) bool {
	gitPath := filepath.Join(candidate, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	common = resolveClean(common)
	if info.IsDir() {
		return resolveClean(gitPath) == common
	}
	// A .git pointer file (linked worktree of a worktree, or moved worktree):
	// the pointer is <common>/worktrees/<id>, two levels below the common dir.
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return false
	}
	gitdir, ok := parseGitdirPointer(string(content))
	if !ok {
		return false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(candidate, gitdir)
	}
	return resolveClean(filepath.Dir(filepath.Dir(gitdir))) == common
}

// resolveClean returns the symlink-resolved, cleaned form of p, falling back to
// a plain Clean when the path cannot be resolved (e.g. it does not exist).
func resolveClean(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

// DirsFromRootToCwd returns the chain of directories from root down to cwd
// inclusive (root first, cwd last). If cwd lies outside root the result is just
// cwd; if root and cwd are equal it is just root.
func DirsFromRootToCwd(root, cwd string) []string {
	root = filepath.Clean(root)
	cwd = filepath.Clean(cwd)

	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return []string{cwd}
	}
	if rel == "." {
		return []string{root}
	}
	// If cwd is outside root, just treat cwd as the only directory.
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return []string{cwd}
	}

	out := []string{root}
	cur := root
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		out = append(out, cur)
	}
	return out
}
