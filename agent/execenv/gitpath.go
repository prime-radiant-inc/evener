package execenv

import (
	"context"
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
