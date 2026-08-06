package execenv

import (
	"context"
	"testing"
)

// BenchmarkGitStatusShellWrapper and BenchmarkGitStatusDirectExec measure the
// per-call cost this task's production change removes: every git invocation
// used to run through the platform shell (Shell(), a bash -c fork wrapping
// the real git fork) before this task added RunGit's direct-exec path
// (Argv(), a single exec.CommandContext("git", ...) fork, no shell in
// between). Both benchmarks run the identical git command
// ("status --porcelain") against the identical real repo, through the same
// execPreparedCommand plumbing — the only variable is the fork mechanism.
func BenchmarkGitStatusShellWrapper(b *testing.B) {
	dir := b.TempDir()
	gitInitBench(b, dir)
	env := NewLocalExecutionEnvironment(dir)
	ctx := context.Background()
	for b.Loop() {
		res, err := env.ExecCommand(ctx, "git status --porcelain", 5000, dir, nil)
		if err != nil || res.ExitCode != 0 {
			b.Fatalf("ExecCommand: err=%v res=%+v", err, res)
		}
	}
}

func BenchmarkGitStatusDirectExec(b *testing.B) {
	dir := b.TempDir()
	gitInitBench(b, dir)
	env := NewLocalExecutionEnvironment(dir)
	ctx := context.Background()
	for b.Loop() {
		res, err := env.ExecArgv(ctx, "git", []string{"status", "--porcelain"}, 5000, dir, nil)
		if err != nil || res.ExitCode != 0 {
			b.Fatalf("ExecArgv: err=%v res=%+v", err, res)
		}
	}
}

func gitInitBench(b *testing.B, dir string) {
	b.Helper()
	if _, err := execLookPath("git"); err != nil {
		b.Skip("git not available")
	}
	cmd := execCommandContext(context.Background(), "git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("git init: %v\n%s", err, out)
	}
}
