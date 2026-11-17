package execenv

import (
	"context"
	"testing"
)

// BenchmarkGitStatusShellWrapper and BenchmarkGitStatusDirectExec run the
// identical git command ("status --porcelain") against the identical real repo
// through the same execPreparedCommand plumbing. The pair was added to measure
// RunGit's direct-exec path (Argv(), one fork, no shell in between) against the
// platform shell every git invocation used to go through. ExecCommand now takes
// the same argv route for a metacharacter-free word list (#6447), so the first
// benchmark now measures the production entry point on its fast path; the
// forced-wrapper comparison lives in git_exec_bench_test.go's
// BenchmarkArgvFastPath pair.
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

// BenchmarkArgvFastPathShellWrapper and BenchmarkArgvFastPathDirectExec measure
// the fork #6447 removes from every shell tool call whose command is a bare word
// list. Both run the same simple command against the same real repository
// through the same execPreparedCommand plumbing; the wrapper arm forces the
// `bash -o pipefail -c` runtime the exec path used to build unconditionally,
// and the direct arm hands the identical words to the same runtime type as argv.
// That isolates the shell fork: the production choice between them is
// commandRuntimeForCommand's, pinned by TestExecCommandChoosesTheRuntimeByCommandString.
// The direct arm pays one extra PATH resolution (launchExecutable resolves a bare
// name, where the wrapper arm's /bin/bash needs none), so it is measured against
// a handicap, not a favour.
func BenchmarkArgvFastPathShellWrapper(b *testing.B) {
	env, dir := newArgvBenchEnv(b)
	ctx := context.Background()
	for b.Loop() {
		res, err := env.execPreparedCommand(ctx, env.commands().Shell("git status --porcelain"), 5000, dir, nil)
		if err != nil || res.ExitCode != 0 {
			b.Fatalf("shell runtime: err=%v res=%+v", err, res)
		}
	}
}

func BenchmarkArgvFastPathDirectExec(b *testing.B) {
	env, dir := newArgvBenchEnv(b)
	ctx := context.Background()
	for b.Loop() {
		res, err := env.execPreparedCommand(ctx, env.commands().Argv("git", "status", "--porcelain"), 5000, dir, nil)
		if err != nil || res.ExitCode != 0 {
			b.Fatalf("argv runtime: err=%v res=%+v", err, res)
		}
	}
}

func newArgvBenchEnv(b *testing.B) (*LocalExecutionEnvironment, string) {
	b.Helper()
	dir := b.TempDir()
	gitInitBench(b, dir)
	env := NewLocalExecutionEnvironment(dir)
	b.Cleanup(env.Cleanup)
	return env, dir
}

// BenchmarkArgvFastPathTrivialShellWrapper and its DirectExec twin isolate the
// fork itself. Nothing in the command's own work competes with it: /bin/true
// costs one exec, so the difference between the pair is the extra
// `bash -o pipefail -c` fork and exit the fast path removes. This pair stays
// readable on a loaded host, where a command with real work (git status) buries
// the delta in scheduler noise.
func BenchmarkArgvFastPathTrivialShellWrapper(b *testing.B) {
	benchTrivial(b, true)
}

func BenchmarkArgvFastPathTrivialDirectExec(b *testing.B) {
	benchTrivial(b, false)
}

func benchTrivial(b *testing.B, shell bool) {
	b.Helper()
	if runtimeGOOS == "windows" {
		b.Skip("POSIX fork comparison only")
	}
	dir := b.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	b.Cleanup(env.Cleanup)
	ctx := context.Background()
	for b.Loop() {
		var res ExecResult
		var err error
		if shell {
			res, err = env.execPreparedCommand(ctx, env.commands().Shell("/bin/true"), 5000, dir, nil)
		} else {
			res, err = env.execPreparedCommand(ctx, env.commands().Argv("/bin/true"), 5000, dir, nil)
		}
		if err != nil || res.ExitCode != 0 {
			b.Fatalf("trivial command: err=%v res=%+v", err, res)
		}
	}
}
