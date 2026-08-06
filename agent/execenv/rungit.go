package execenv

import "context"

// RunGit runs `git <args...>` in env. When env implements ArgvExecutor (the
// real LocalExecutionEnvironment always does) it execs git directly, argv in
// hand, skipping the platform-shell fork ExecCommand pays for every call and
// removing the shell-string-interpolation surface that building "git "+args
// would otherwise reopen. Environments that don't implement ArgvExecutor
// (test fakes, sandboxed/remote environments this package doesn't control)
// fall back to ExecCommand with each arg shell-escaped, exactly as every git
// call site behaved before this function existed. Both paths preserve
// identical stdout/stderr/exit-code/timeout/cancellation semantics — only the
// fork mechanism differs — because both terminate in the same
// execPreparedCommand.
func RunGit(ctx context.Context, env ExecutionEnvironment, workingDir string, timeoutMS int, args ...string) (ExecResult, error) {
	if direct, ok := env.(ArgvExecutor); ok {
		return direct.ExecArgv(ctx, "git", args, timeoutMS, workingDir, nil)
	}
	return env.ExecCommand(ctx, "git "+shellEscapeArgs(args...), timeoutMS, workingDir, nil)
}
