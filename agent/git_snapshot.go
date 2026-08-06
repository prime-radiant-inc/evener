package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/agent/execenv"
)

// gitExecTimeout bounds every git subprocess exec this file runs for the
// snapshot (both the context deadline and the equal timeoutMS each exec
// passes to ExecCommand — ExecCommand arms its own timer from timeoutMS,
// independent of the context, so both must derive from the same value for
// the bound to hold). It is a package-level var rather than a const solely
// so tests can widen it for the duration of a single test (via a
// t.Cleanup-scoped reassignment) when scheduler contention would otherwise
// starve a trivial git invocation past a fixed deadline unrelated to the
// behavior under test — see execenv.gitExecTimeout for the sibling case this
// mirrors. Production always runs with this 2s default.
var gitExecTimeout = 2 * time.Second

// gitExecTimeoutMS is gitExecTimeout in the milliseconds ExecCommand takes.
func gitExecTimeoutMS() int { return int(gitExecTimeout / time.Millisecond) }

// gitOriginURL returns the git remote origin URL for the repo at cwd,
// or "" if not a git repo or no origin remote is configured.
func gitOriginURL(env execenv.ExecutionEnvironment, cwd string) string {
	if env == nil {
		return ""
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = env.WorkingDirectory()
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
	defer cancel()
	res, err := execenv.RunGit(ctx, env, cwd, gitExecTimeoutMS(), "remote", "get-url", "origin")
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

func snapshotGit(env execenv.ExecutionEnvironment, cwd string) (inRepo bool, branch string, modifiedFiles int, untrackedFiles int, recentCommitTitles []string) {
	if env == nil {
		return false, "", 0, 0, nil
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = env.WorkingDirectory()
	}
	if !hasGitMetadataAncestor(cwd) {
		return false, "", 0, 0, nil
	}

	run := func(args ...string) (execenv.ExecResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
		defer cancel()
		return execenv.RunGit(ctx, env, cwd, gitExecTimeoutMS(), args...)
	}

	inside, err := run("rev-parse", "--is-inside-work-tree")
	if err != nil || inside.ExitCode != 0 || strings.TrimSpace(inside.Stdout) != "true" {
		return false, "", 0, 0, nil
	}
	inRepo = true

	if br, err := run("rev-parse", "--abbrev-ref", "HEAD"); err == nil && br.ExitCode == 0 {
		branch = strings.TrimSpace(br.Stdout)
	}

	if st, err := run("status", "--porcelain"); err == nil && st.ExitCode == 0 {
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

	// A literal space in the format is safe now: this runs via direct argv
	// exec, not a shell command line, so there's no word-splitting to dodge
	// (the %x20 trick this replaced existed solely to survive that splitting).
	if lg, err := run("log", "-n", "5", "--pretty=format:%h %s"); err == nil && lg.ExitCode == 0 {
		for line := range strings.SplitSeq(strings.ReplaceAll(lg.Stdout, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			recentCommitTitles = append(recentCommitTitles, line)
		}
	}

	return inRepo, branch, modifiedFiles, untrackedFiles, recentCommitTitles
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
