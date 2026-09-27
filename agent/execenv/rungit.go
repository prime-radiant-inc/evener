package execenv

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errRunGitShellUnsupported reports that RunGit refused to build a shell
// command line because the environment's platform shell cannot be trusted with
// ShellEscapeArgs' POSIX quoting. It is unexported: callers of RunGit already
// treat any error as "git did not run", and the refusal returns the zero
// ExecResult alongside it so even a caller that checks ExitCode before err
// cannot read a synthetic git exit status into the refusal.
var errRunGitShellUnsupported = errors.New("execenv: RunGit cannot build a shell command line for this platform")

// RunGit runs `git <args...>` in env. When env implements ArgvExecutor (the
// real LocalExecutionEnvironment always does) it execs git directly, argv in
// hand, skipping the platform-shell fork ExecCommand pays for every call and
// removing the shell-string-interpolation surface that building "git "+args
// would otherwise reopen. Environments that don't implement ArgvExecutor
// (test fakes, sandboxed/remote environments this package doesn't control)
// fall back to ExecCommand with each arg shell-escaped, exactly as every git
// call site behaved before this function existed — except where that escaping
// buys nothing: ShellEscapeArgs renders POSIX words, cmd.exe treats a single
// quote as ordinary text and still expands %VAR% (see execsupport/shellquote's
// "POSIX shells only" section), so an environment whose Platform reports
// Windows is refused rather than handed a command line whose metacharacters
// survive. The executed paths preserve identical
// stdout/stderr/exit-code/timeout/cancellation semantics — only the fork
// mechanism differs — because both terminate in the same
// execPreparedCommand. An error means git did not run: the Windows refusal
// returns the zero ExecResult with its error, so a caller may inspect ExitCode
// before err without reading a synthetic status into a refusal.
func RunGit(ctx context.Context, env ExecutionEnvironment, workingDir string, timeoutMS int, args ...string) (ExecResult, error) {
	if direct, ok := env.(ArgvExecutor); ok {
		return direct.ExecArgv(ctx, "git", args, timeoutMS, workingDir, nil)
	}
	return runGitViaShell(ctx, env, env.Platform(), workingDir, timeoutMS, args...)
}

// runGitViaShell is RunGit's fallback for an environment that does not
// implement ArgvExecutor. platform is an explicit argument rather than a read
// of runtime.GOOS so both branches are testable on any host, and because the
// shell at issue is the one this environment's ExecCommand forks — for the
// local environment that is cmd.exe on a Windows host (see shellCommand).
// Windows is refused: the POSIX quoting ShellEscapeArgs emits is not quoting
// for cmd.exe, so invoking the fallback there would leave `&`, `|`, and
// `%VAR%` live in a command string assembled from caller-supplied arguments.
// The refusal returns the zero ExecResult with the error: an error from RunGit
// means git did not run, so there is no git exit status to report, and a caller
// that inspects ExitCode before err still reaches the error below instead of
// reporting "git <args>: exit 127".
func runGitViaShell(ctx context.Context, env ExecutionEnvironment, platform, workingDir string, timeoutMS int, args ...string) (ExecResult, error) {
	if strings.ToLower(strings.TrimSpace(platform)) == "windows" {
		return ExecResult{}, fmt.Errorf(
			"%w: environment reports platform %q, whose shell does not honor POSIX quoting, so the escaped arguments would remain injectable; this environment must implement ArgvExecutor",
			errRunGitShellUnsupported, platform,
		)
	}
	return env.ExecCommand(ctx, "git "+ShellEscapeArgs(args...), timeoutMS, workingDir, nil)
}
