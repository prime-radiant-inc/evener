package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/identifier"
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
	return GitRootOrEmptyContext(context.Background(), env, cwd)
}

// GitRootOrEmptyContext is GitRootOrEmpty under the caller's work context:
// cancelling ctx stops the `git rev-parse` fallback instead of leaving it to
// run out its own timeout. Callers whose work must stop when their session
// closes use this; the rest keep the shorter spelling.
func GitRootOrEmptyContext(ctx context.Context, env ExecutionEnvironment, cwd string) string {
	// Memoize per environment: a session resolves the git root several times at
	// init, all on the same env and cwd, so fork `git rev-parse` once.
	if local, ok := env.(*LocalExecutionEnvironment); ok && local.gitRoots != nil {
		return local.gitRoots.lookup(cwd, func() (string, bool) { return gitRootUncached(ctx, env, cwd) })
	}
	root, _ := gitRootUncached(ctx, env, cwd)
	return root
}

// GitRootOrEmptyFreshDefinitive is GitRootOrEmptyContext WITHOUT the
// per-environment memo, for a caller asking whether a workspace has become a
// repository since the session started. It keeps the resolver's definitive
// flag, for a caller whose answer depends on the DIFFERENCE between
// "this is not a repository" and "I could not find out".
//
// The memo exists to spare repeated forks at startup, and it is right for that:
// a directory's answer does not change under a running session often. It is
// wrong for a freshness check, which is asking precisely whether it has. A
// session launched before `git init` would otherwise read the cached "no
// repository" for the rest of its life and never notice the repository, and the
// project MCP layer, that appeared around it.
//
// The flag is not decoration. A freshness check that reads a bare empty root as
// "confirmed no repository" caches an absence nothing established: the
// resolution that timed out or was cancelled says only that it could not ask,
// and a worktree with a matching MCP fallback may well be sitting there. The
// caller must keep the question open, which means it needs this flag.
func GitRootOrEmptyFreshDefinitive(env ExecutionEnvironment, cwd string) (string, bool) {
	if env == nil {
		return "", false
	}
	return gitRootUncached(context.Background(), env, cwd)
}

// gitAnswered reports whether err from a git invocation is git's OWN answer — a
// process that ran and chose its exit status — rather than a failure to obtain
// an answer at all. It is what decides whether a resolution may be memoized
// (gitRootCache.lookup): a verdict about a directory is stable and worth
// remembering, while "we could not ask" is a property of one moment, and
// caching that makes an environment believe a directory is not a repository
// long after the request whose cancellation caused it is gone.
//
// nil is an answer. A cancelled or expired context is not, nor is the error the
// runner substitutes when it gives up — so the context is consulted directly as
// well as through the error. A process killed by a signal never chose its
// status, so a wrapper or the runner ending git is not git answering. Anything
// that is not an exit status at all (binary missing, EAGAIN, a transport
// failure from a non-local environment) produced no verdict either.
func gitAnswered(ctx context.Context, err error) bool {
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return false
	}
	// Exited() is promoted from the embedded *os.ProcessState, which a
	// hand-built ExitError could leave nil.
	return exitErr.ProcessState != nil && exitErr.Exited()
}

// gitRootUncached resolves cwd's working-tree root. definitive says whether the
// answer is one to remember (see gitRootCache.lookup): a structural resolution,
// an absence the filesystem actually reported, and a verdict git itself returned
// all are. A fork that never ran or never finished is not, and neither is a
// filesystem that could not answer — see gitAnswered and
// structuralWorktreeRootDetailed for the classifications this composes.
func gitRootUncached(ctx context.Context, env ExecutionEnvironment, cwd string) (root string, definitive bool) {
	if _, ok := env.(*LocalExecutionEnvironment); ok {
		structural, outcome := structuralWorktreeRootDetailed(cwd)
		switch outcome {
		case worktreeRootFound:
			return structural, true
		case worktreeRootAbsent:
			// One walk established that no ".git" entry exists in any ancestor.
			return "", true
		case worktreeRootRefused:
			// Refused pointer or unresolved stat: unknown, not absent. Cache
			// nothing, and do not hand the question to the binary.
			return "", false
		}
		// worktreeRootNonPointer: a readable ".git" that is not a gitdir
		// pointer is left to the binary below.
	}

	execCtx, cancel := context.WithTimeout(ctx, gitExecTimeout)
	defer cancel()

	res, err := RunGit(execCtx, env, cwd, gitExecTimeoutMS(), "rev-parse", "--show-toplevel")
	if err != nil {
		// A non-zero exit reaches us as an error value, so "error" alone cannot
		// mean "no answer": git refusing a directory (exit 128) is a verdict, and
		// a permanent one. Classify by what the error IS.
		return "", gitAnswered(execCtx, err)
	}
	if res.ExitCode != 0 {
		return "", true // git ran and said this is not a repository
	}
	// A truncated STDOUT capture is a partial path, not a verdict: report it
	// unknown rather than resolve a wrong root from head and tail fragments
	// (#4568 round 2). The root is read from stdout, so a capped STDERR is
	// git's diagnostics and changes nothing (#4621 round 16).
	if res.StdoutTruncated {
		return "", false
	}
	root = strings.TrimSpace(res.Stdout)
	if root == "" {
		return "", true
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
		return "", true // git answered, and its answer failed the sanity check
	}
	return root, true
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
		return local.mainRoots.lookup(cwd, func() (string, bool) { return mainRepoRootUncached(env, cwd) })
	}
	root, _ := mainRepoRootUncached(env, cwd)
	return root
}

// ResolveMainRepoRootFresh is ResolveMainRepoRoot WITHOUT the per-environment
// memo, for a caller asking what the workspace is NOW rather than what it was
// when the environment last asked.
//
// The memo is keyed by directory, and a directory's identity can change under a
// running session: `git init` in a subdirectory of a checkout makes that
// subdirectory its own repository, and every later question about it has a new
// answer. A caller that compares identities across a launch — was this layer's
// repository the one the launch authorized? — must not answer it from the
// launch's memo, or a repository created since the launch inherits the enclosing
// repository's identity and with it the enclosing repository's trust.
//
// The cost is one structural walk, or a git fork when the walk cannot decide,
// and callers are those already resolving a project config path afresh.
func ResolveMainRepoRootFresh(env ExecutionEnvironment, cwd string) string {
	if env == nil {
		return ""
	}
	root, _ := mainRepoRootUncached(env, cwd)
	return root
}

// mainRepoRootUncached resolves cwd's main repository root, reporting whether
// the answer is definitive on the same terms gitRootUncached does: it shares
// the cache, so a resolution error must not be memoized here either.
func mainRepoRootUncached(env ExecutionEnvironment, cwd string) (root string, definitive bool) {
	root, isGit, err := resolveMainRepoRoot(env, cwd)
	if err != nil {
		// The same classification gitRootUncached makes, on the same cache. The
		// resolver wraps its causes with %w, so git's exit status is still
		// reachable through them; its own structural errors (an unreadable
		// pointer file, a root that does not contain cwd) are not exit statuses
		// and stay uncached, which costs a re-resolution rather than a wrong
		// permanent answer.
		return "", gitAnswered(context.Background(), err)
	}
	if !isGit {
		return "", true
	}
	return root, true
}

// worktreeRootOutcome is what one structural walk learned about the ".git"
// entry chain from cwd to the filesystem root.
type worktreeRootOutcome int

const (
	// worktreeRootAbsent: no ".git" entry exists in cwd or any ancestor.
	worktreeRootAbsent worktreeRootOutcome = iota
	// worktreeRootFound: an entry resolved cwd's working-tree root.
	worktreeRootFound
	// worktreeRootRefused: an entry exists but could not be read, or a stat
	// failed for a reason other than absence. Unresolved, not absent.
	worktreeRootRefused
	// worktreeRootNonPointer: an entry was read and is not a gitdir pointer.
	// The Git binary may still classify it.
	worktreeRootNonPointer
)

// structuralWorktreeRoot is structuralWorktreeRootDetailed for callers that
// only ask whether a root was found.
func structuralWorktreeRoot(cwd string) (string, bool) {
	root, outcome := structuralWorktreeRootDetailed(cwd)
	return root, outcome == worktreeRootFound
}

// structuralWorktreeRootDetailed walks cwd and its ancestors for a ".git"
// entry, ONCE. The outcome distinguishes a resolved root, a refused read, a
// readable entry that is not a gitdir pointer, and an absence the walk observed.
// The walk is the only ancestor probe: a second probe would leave a window in
// which the workspace can plant a ".git" entry the later probe sees but never
// reads, and the Git binary behind it resolves from a PATH the workspace can
// reach. A refused read or an unresolved stat is not "no repository here": the
// caller must leave the question open rather than fall through to that binary.
func structuralWorktreeRootDetailed(cwd string) (root string, outcome worktreeRootOutcome) {
	dir := filepath.Clean(cwd)
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return identifier.ResolveClean(dir), worktreeRootFound
			}
			// Read through the pointer reader, not os.ReadFile. This runs from
			// the FRESH resolution: child construction asks for the root
			// uncached so a repository that appeared after the parent started
			// is still found, and it does so synchronously, before any
			// subprocess timeout applies. A ".git" entry that has become a FIFO
			// — a shared workspace another process can write into — makes a
			// plain read block forever, so the child's discovery would hold
			// delegate creation open past Stop. The pointer reader opens
			// non-blocking and admits only a regular file, so a pointer that is
			// not one answers "unreadable" here.
			content, err := ReadGitPointerFile(gitPath)
			if err != nil {
				return "", worktreeRootRefused
			}
			if _, ok := identifier.ParseGitdirPointer(string(content)); ok {
				return identifier.ResolveClean(dir), worktreeRootFound
			}
			// Read fine but not a gitdir pointer: an ordinary working tree
			// marker git itself can classify, so keep the binary fallback.
			return "", worktreeRootNonPointer
		}
		if !errors.Is(err, fs.ErrNotExist) {
			// Only an absence the filesystem actually reported lets the walk
			// move on; anything else leaves it unknown whether a ".git" is
			// here, and a fallback would hand that unknown to the binary.
			return "", worktreeRootRefused
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", worktreeRootAbsent
		}
		dir = parent
	}
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
