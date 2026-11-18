package agent

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

// gitProductionExecTimeout bounds every git subprocess exec this file runs for
// the snapshot (both the context deadline and the equal timeoutMS each exec
// passes to ExecCommand — ExecCommand arms its own timer from timeoutMS,
// independent of the context, so both must derive from the same value for the
// bound to hold): session launch must not stall on a slow repository.
const gitProductionExecTimeout = 2 * time.Second

// gitTestExecTimeout is the bound under `go test`. -race on a loaded runner can
// starve a trivial git invocation past a fixed deadline unrelated to the
// behavior under test, and snapshotGit reports a failed exec as "no data" —
// indistinguishable from an empty result (see #4132's note on IsGitRepo) — so
// the production 2s bound flakes every test that asserts the snapshot's data.
// 30s matches this package's standing whole-operation tripwire (see
// memoryTestBoundaryBudget), far above what the work costs at rest, so only a
// genuine hang reaches it. This mirrors memoryBoundaryWait (session_memory.go),
// the same production/test split for the memory boundary's read budget.
//
// It is the default for every `go test` binary that links this package, not one
// fixture, because a fixture that asserts the snapshot's data runs in a PARALLEL
// test: widening the var for one test (execenv's SetGitExecTimeoutForTesting
// idiom) would be a parallel write to a package-level var, a data race against
// the parallel readers.
const gitTestExecTimeout = 30 * time.Second

// gitExecTimeout is gitProductionExecTimeout in production and
// gitTestExecTimeout under `go test` (testing.Testing() is false outside a
// `go test` binary, so production keeps the 2s bound). It stays a package-level
// var so a test whose subject IS the bound can set it back to
// gitProductionExecTimeout for its duration — serially, and with the restore
// registered before the fixture runs so a fatal cannot leak it (issue #4132).
var gitExecTimeout = defaultGitExecTimeout()

// defaultGitExecTimeout picks this process's git exec bound: the long test
// budget under `go test`, the production bound otherwise.
func defaultGitExecTimeout() time.Duration {
	if testing.Testing() {
		return gitTestExecTimeout
	}
	return gitProductionExecTimeout
}

// gitExecTimeoutMS is gitExecTimeout in the milliseconds ExecCommand takes.
func gitExecTimeoutMS() int { return int(gitExecTimeout / time.Millisecond) }

func runSnapshotGit(ctx context.Context, env execenv.ExecutionEnvironment, cwd string, args ...string) (execenv.ExecResult, error) {
	execCtx, cancel := context.WithTimeout(ctx, gitExecTimeout)
	defer cancel()
	return execenv.RunGit(execCtx, env, cwd, gitExecTimeoutMS(), args...)
}

// gitOriginURL returns the git remote origin URL for the repo at cwd,
// or "" if not a git repo or no origin remote is configured. ctx is the
// caller's work context: each exec is additionally bounded by gitExecTimeout,
// and cancelling ctx stops the fork in flight.
func gitOriginURL(ctx context.Context, env execenv.ExecutionEnvironment, cwd string) string {
	if env == nil {
		return ""
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = env.WorkingDirectory()
	}
	res, err := runSnapshotGit(ctx, env, cwd, "remote", "get-url", "origin")
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// snapshotGitBranch reads only the current branch name for cwd, skipping the
// status/log/origin-url subprocess calls snapshotGit also runs. Used by the
// envctx GitBranch probe (agent/session_init.go), which runs on every user
// turn and needs just the branch name, not a full session-launch environment
// snapshot. An empty branch is known only when the directory is outside
// a worktree. An unborn HEAD or an interrupted probe leaves it unknown.
func snapshotGitBranch(env execenv.ExecutionEnvironment, cwd string) (branch string, known bool) {
	if env == nil {
		return "", false
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = env.WorkingDirectory()
	}
	inRepo, branch, failed := snapshotGitCheckout(context.Background(), env, cwd)
	return branch, !failed && (!inRepo || branch != "")
}

// Launch and request-time probes use the same evidence for checkout membership.
// Git refusing to inspect an existing checkout leaves its branch unknown.
func snapshotGitCheckout(ctx context.Context, env execenv.ExecutionEnvironment, cwd string) (inRepo bool, branch string, probeFailed bool) {
	if !hasGitMetadataAncestor(cwd) {
		return false, "", false
	}
	inside, err := runSnapshotGit(ctx, env, cwd, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside.ExitCode != 0 || inside.TimedOut || inside.StdoutTruncated {
		return false, "", true
	}
	if membership := strings.TrimSpace(inside.Stdout); membership != "true" {
		return false, "", membership != "false"
	}
	head, err := runSnapshotGit(ctx, env, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if head.TimedOut || head.StdoutTruncated || (err != nil && !gitExited(err)) {
		return true, "", true
	}
	if err != nil || head.ExitCode != 0 {
		// A worktree can have an unborn HEAD.
		return true, "", false
	}
	return true, strings.TrimSpace(head.Stdout), false
}

// snapshotGit reads the launch/refresh git summary for cwd. ctx is the
// caller's work context: each exec is additionally bounded by gitExecTimeout,
// and cancelling ctx stops the forks in flight and the ones still to come.
//
// probeFailed reports that a probe could not run (exec error, timeout,
// cancellation) or was cut short, and so its empty value is not the answer
// "nothing there". A failed membership probe leaves the checkout unknown.
// Within a confirmed checkout, an unborn HEAD can legitimately produce a
// nonzero branch or log exit.
func snapshotGit(ctx context.Context, env execenv.ExecutionEnvironment, cwd string) (inRepo bool, branch string, modifiedFiles int, untrackedFiles int, recentCommitTitles []string, statusKnown bool, probeFailed bool) {
	if env == nil {
		return false, "", 0, 0, nil, false, false
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = env.WorkingDirectory()
	}
	inRepo, branch, probeFailed = snapshotGitCheckout(ctx, env, cwd)
	if !inRepo {
		return false, "", 0, 0, nil, false, probeFailed
	}

	// A truncated STDOUT capture is refused, not decoded: counting a partial
	// status stream would report a smaller change set than the repository holds.
	// Like a failed exec above, a truncated one yields no counts rather than a
	// wrong one (#4568 round 2). A capped STDERR is not the status parser's
	// business — it reads stdout — so a noisy stderr no longer discards counts
	// that were read whole (#4621 round 15).
	if st, err := runSnapshotGit(ctx, env, cwd, "status", "--porcelain"); err != nil && !gitExited(err) {
		probeFailed = true
	} else if err == nil && st.ExitCode == 0 && !st.StdoutTruncated {
		statusKnown = true
		for line := range strings.SplitSeq(strings.ReplaceAll(st.Stdout, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "?? ") {
				untrackedFiles++
				continue
			}
			modifiedFiles++
		}
	}

	// %x20 for a literal space is no longer needed to survive shell
	// word-splitting (this runs via direct argv exec now), but it's kept
	// anyway: git parses %x20 and a literal space identically, and keeping it
	// matches the fixed string the evenerfuzz workspace-prompt fixture
	// (workspace_prompt_program_fuzz_test.go) and git_snapshot_test.go key
	// their scripted responses on — switching it would be pure fixture churn
	// for no behavior change.
	lg, err := runSnapshotGit(ctx, env, cwd, "log", "-n", "5", "--pretty=format:%h%x20%s")
	switch {
	case err != nil && !gitExited(err):
		// git did not run: an empty commit list here would claim "no commits".
		probeFailed = true
	case err == nil && lg.ExitCode == 0 && lg.StdoutTruncated:
		// A partial log is not "no commits" either; the trusted stream was cut.
		probeFailed = true
	case err == nil && lg.ExitCode == 0:
		for line := range strings.SplitSeq(strings.ReplaceAll(lg.Stdout, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			recentCommitTitles = append(recentCommitTitles, line)
		}
	}

	return inRepo, branch, modifiedFiles, untrackedFiles, recentCommitTitles, statusKnown, probeFailed
}

// gitExited reports that err is git's completed nonzero exit rather than a
// failure to run it. LocalExecutionEnvironment returns the exec.ExitError
// alongside the ExecResult, so RunGit's "an error means git did not run"
// contract does not hold for the real environment; a fresh repository's
// `rev-parse` and `log` exit nonzero as normal answers (#4517 review).
//
// A process killed by a signal also arrives as *exec.ExitError, but it never
// answered. Requiring ProcessState.Exited() separates that interruption (the
// OOM killer during launch, for example) from a completed nonzero exit, the
// same way execenv/gitpath.go's gitAnswered does.
func gitExited(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	// Exited() is promoted from the embedded *os.ProcessState, which a
	// hand-built ExitError could leave nil.
	return exitErr.ProcessState != nil && exitErr.Exited()
}

func hasGitMetadataAncestor(cwd string) bool {
	dir := strings.TrimSpace(cwd)
	if dir == "" || !filepath.IsAbs(dir) {
		return true
	}
	// A filesystem error leaves the decision to Git.
	present, known := execenv.HasGitEntryAncestor(dir)
	return present || !known
}
