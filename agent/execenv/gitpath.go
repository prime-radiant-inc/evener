package execenv

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/internal/gitpath"
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
//
// This is a thin wrapper around internal/gitpath.StructuralMainRoot, kept
// under its established local name so this package's tests and fuzz targets
// can keep referencing it directly.
func structuralMainRoot(cwd string) (string, bool) {
	return gitpath.StructuralMainRoot(cwd)
}

// mainRootFromGitdirPointer parses a linked worktree's ".git" pointer file
// content ("gitdir: <path>") and returns the main repository root iff the
// pointer resolves to the ".git/worktrees/<id>" shape. Relative pointer paths
// are resolved against ancestorDir (the directory holding the .git file). It is
// a pure function (no filesystem access), so it is trivially fuzzable.
//
// Thin wrapper around internal/gitpath.MainRootFromGitdirPointer; see
// structuralMainRoot for why the wrapper exists.
func mainRootFromGitdirPointer(pointerContent, ancestorDir string) (string, bool) {
	return gitpath.MainRootFromGitdirPointer(pointerContent, ancestorDir)
}

// parseGitdirPointer extracts the path from the first "gitdir: <path>" line of a
// git pointer file. Returns ok=false when no non-empty gitdir line is present.
//
// Thin wrapper around internal/gitpath.ParseGitdirPointer; see
// structuralMainRoot for why the wrapper exists.
func parseGitdirPointer(content string) (string, bool) {
	return gitpath.ParseGitdirPointer(content)
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
//
// Thin wrapper around internal/gitpath.MainRootCandidateFromCommonDir; see
// structuralMainRoot for why the wrapper exists.
func mainRootCandidateFromCommonDir(cwd, common string) string {
	return gitpath.MainRootCandidateFromCommonDir(cwd, common)
}

// gitEntryResolvesToCommon reports whether candidate holds a .git entry that
// resolves back to the given common .git dir. This distinguishes a genuine main
// repo root (candidate/.git IS the common dir) from a submodule's fake candidate
// (<super>/.git/modules, which has no .git entry of its own).
//
// Thin wrapper around internal/gitpath.GitEntryResolvesToCommon; see
// structuralMainRoot for why the wrapper exists.
func gitEntryResolvesToCommon(candidate, common string) bool {
	return gitpath.GitEntryResolvesToCommon(candidate, common)
}

// resolveClean returns the symlink-resolved, cleaned form of p, falling back to
// a plain Clean when the path cannot be resolved (e.g. it does not exist).
//
// Thin wrapper around internal/gitpath.ResolveClean; see structuralMainRoot
// for why the wrapper exists.
func resolveClean(p string) string {
	return gitpath.ResolveClean(p)
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
