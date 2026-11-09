package agent

import (
	"context"
	"os"
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
	execCtx, cancel := context.WithTimeout(ctx, gitExecTimeout)
	defer cancel()
	res, err := execenv.RunGit(execCtx, env, cwd, gitExecTimeoutMS(), "remote", "get-url", "origin")
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// snapshotGitBranch reads only the current branch name for cwd, skipping the
// status/log/origin-url subprocess calls snapshotGit also runs. Used by the
// envctx GitBranch probe (agent/session_init.go), which runs on every user
// turn and needs just the branch name, not a full session-launch environment
// snapshot.
func snapshotGitBranch(env execenv.ExecutionEnvironment, cwd string) string {
	if env == nil {
		return ""
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = env.WorkingDirectory()
	}
	if !hasGitMetadataAncestor(cwd) {
		return ""
	}
	run := func(cmd string) (execenv.ExecResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
		defer cancel()
		return env.ExecCommand(ctx, cmd, gitExecTimeoutMS(), cwd, nil)
	}
	inside, err := run("git rev-parse --is-inside-work-tree")
	if err != nil || inside.ExitCode != 0 || strings.TrimSpace(inside.Stdout) != "true" {
		return ""
	}
	br, err := run("git rev-parse --abbrev-ref HEAD")
	if err != nil || br.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(br.Stdout)
}

// snapshotGit reads the launch/refresh git summary for cwd. ctx is the
// caller's work context: each exec is additionally bounded by gitExecTimeout,
// and cancelling ctx stops the forks in flight and the ones still to come.
//
// probeFailed reports that a probe could not run (exec error, timeout,
// cancellation) or was cut short, and so its empty value is not the answer
// "nothing there". It is the #4517 distinction `execenv.RunGit` already makes
// at its source ("an error means git did not run"): a failed `git log` is not
// an empty repository, and a failed `rev-parse` is not a non-repository. A
// non-zero EXIT is different again — git ran and answered, so an empty
// repository's log stays an empty commit list, not a probe failure.
func snapshotGit(ctx context.Context, env execenv.ExecutionEnvironment, cwd string) (inRepo bool, branch string, modifiedFiles int, untrackedFiles int, recentCommitTitles []string, statusKnown bool, probeFailed bool) {
	if env == nil {
		return false, "", 0, 0, nil, false, false
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = env.WorkingDirectory()
	}
	if !hasGitMetadataAncestor(cwd) {
		return false, "", 0, 0, nil, false, false
	}

	run := func(args ...string) (execenv.ExecResult, error) {
		execCtx, cancel := context.WithTimeout(ctx, gitExecTimeout)
		defer cancel()
		return execenv.RunGit(execCtx, env, cwd, gitExecTimeoutMS(), args...)
	}

	inside, err := run("rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false, "", 0, 0, nil, false, true
	}
	if inside.ExitCode != 0 || strings.TrimSpace(inside.Stdout) != "true" {
		return false, "", 0, 0, nil, false, false
	}
	inRepo = true

	if br, err := run("rev-parse", "--abbrev-ref", "HEAD"); err != nil {
		probeFailed = true
	} else if br.ExitCode == 0 {
		branch = strings.TrimSpace(br.Stdout)
	}

	// A truncated STDOUT capture is refused, not decoded: counting a partial
	// status stream would report a smaller change set than the repository holds.
	// Like a failed exec above, a truncated one yields no counts rather than a
	// wrong one (#4568 round 2). A capped STDERR is not the status parser's
	// business — it reads stdout — so a noisy stderr no longer discards counts
	// that were read whole (#4621 round 15).
	if st, err := run("status", "--porcelain"); err != nil {
		probeFailed = true
	} else if st.ExitCode == 0 && !st.StdoutTruncated {
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
	lg, err := run("log", "-n", "5", "--pretty=format:%h%x20%s")
	switch {
	case err != nil:
		// git did not run: an empty commit list here would claim "no commits".
		probeFailed = true
	case lg.ExitCode == 0 && lg.StdoutTruncated:
		// A partial log is not "no commits" either; the trusted stream was cut.
		probeFailed = true
	case lg.ExitCode == 0:
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

func hasGitMetadataAncestor(cwd string) bool {
	dir := strings.TrimSpace(cwd)
	if dir == "" || !filepath.IsAbs(dir) {
		return true
	}
	dir = filepath.Clean(dir)
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
