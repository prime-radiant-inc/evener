package execenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/identifier"
)

// gitExecTimeout bounds every git subprocess exec this file runs to resolve a
// repo/worktree root — both the context deadline and the equal timeoutMS each
// exec passes to ExecCommand (ExecCommand arms its own timer from timeoutMS,
// independent of the context, so both must derive from the same value for the
// bound to hold). It is a package-level var rather than a const solely so
// tests can widen it for the duration of a single test (via t.Cleanup-scoped
// reassignment) when scheduler contention from sibling packages' tests would
// otherwise starve a trivial git invocation past a fixed deadline unrelated to
// the behavior under test. Production always runs with this 2s default.
var gitExecTimeout = 2 * time.Second

// gitExecTimeoutMS is gitExecTimeout in the milliseconds ExecCommand takes.
func gitExecTimeoutMS() int { return int(gitExecTimeout / time.Millisecond) }

// SetGitExecTimeoutForTesting overrides gitExecTimeout for the duration of a
// test, returning a restore func. gitExecTimeout is unexported, so callers
// outside this package (which cannot reassign it directly the way this
// package's own tests do) use this to widen it — see gitExecTimeout's doc
// comment for why a test would ever need to.
func SetGitExecTimeoutForTesting(d time.Duration) (restore func()) {
	orig := gitExecTimeout
	gitExecTimeout = d
	return func() { gitExecTimeout = orig }
}

// GitRootOrEmpty returns the absolute path of the git working-tree root
// containing cwd, or "" if cwd is not inside a git repository (or git is
// unavailable). For local environments it resolves ordinary working trees
// structurally before falling back to `git rev-parse --show-toplevel` in env
// with a short timeout. The fallback resolves symlinks and sanity-checks that
// the reported root is a prefix of cwd.
func GitRootOrEmpty(env ExecutionEnvironment, cwd string) string {
	// Memoize per environment: a session resolves the git root several times at
	// init, all on the same env and cwd, so fork `git rev-parse` once.
	if local, ok := env.(*LocalExecutionEnvironment); ok && local.gitRoots != nil {
		return local.gitRoots.lookup(cwd, func() string { return gitRootUncached(env, cwd) })
	}
	return gitRootUncached(env, cwd)
}

func gitRootUncached(env ExecutionEnvironment, cwd string) string {
	if _, ok := env.(*LocalExecutionEnvironment); ok {
		if root, ok := structuralWorktreeRoot(cwd); ok {
			return root
		}
		if !hasGitEntryAncestor(cwd) {
			return ""
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
	defer cancel()

	res, err := RunGit(ctx, env, cwd, gitExecTimeoutMS(), "rev-parse", "--show-toplevel")
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
	root, isGit, err := resolveMainRepoRoot(env, cwd)
	if err != nil || !isGit {
		return ""
	}
	return root
}

func structuralWorktreeRoot(cwd string) (string, bool) {
	dir := filepath.Clean(cwd)
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return resolveClean(dir), true
			}
			content, err := os.ReadFile(gitPath)
			if err == nil {
				if _, ok := identifier.ParseGitdirPointer(string(content)); ok {
					return resolveClean(dir), true
				}
			}
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func hasGitEntryAncestor(cwd string) bool {
	dir := filepath.Clean(cwd)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// mainRootFromGitdirPointer parses a linked worktree's ".git" pointer file
// content ("gitdir: <path>") and returns the main repository root iff the
// pointer resolves to the ".git/worktrees/<id>" shape. Relative pointer paths
// are resolved against ancestorDir (the directory holding the .git file). It is
// a pure function (no filesystem access), so it is trivially fuzzable.
//
// Compatibility wrapper around the shared identifier helper.
func mainRootFromGitdirPointer(pointerContent, ancestorDir string) (string, bool) {
	return identifier.MainRootFromGitdirPointer(pointerContent, ancestorDir)
}

// parseGitdirPointer extracts the path from the first "gitdir: <path>" line of a
// git pointer file. Returns ok=false when no non-empty gitdir line is present.
//
// Compatibility wrapper around the shared identifier helper.
func parseGitdirPointer(content string) (string, bool) {
	return identifier.ParseGitdirPointer(content)
}

// mainRootCandidateFromCommonDir turns a `git rev-parse --git-common-dir` result
// into a candidate main repo root: the parent of the common .git dir. The output
// is relative from a main checkout but absolute from a linked worktree, so cwd
// is joined only for the relative case (joining an absolute common with cwd
// would mangle it into "<cwd>/<abs>"). The shared helper performs lexical
// normalization only; local callers validate symlinks separately.
//
// Compatibility wrapper around the shared identifier helper.
func mainRootCandidateFromCommonDir(cwd, common string) string {
	return identifier.MainRootCandidateFromCommonDir(cwd, common)
}

// resolveClean returns the symlink-resolved, cleaned form of p, falling back to
// a plain Clean when the path cannot be resolved (e.g. it does not exist).
//
// Compatibility wrapper retaining the old local normalization seam.
func resolveClean(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
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
	for _, p := range splitPathComponents(rel, string(filepath.Separator)) {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		out = append(out, cur)
	}
	return out
}

var splitPathComponents = strings.Split
