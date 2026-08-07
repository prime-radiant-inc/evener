package execenv

import (
	"context"
	"os/exec"
	"testing"
)

// TestRunGitUsesArgvExecutorDirectly proves RunGit prefers the ArgvExecutor
// capability when env supports it: the scripted env records the exact argv,
// name, timeout, cwd, and env it received, with no shell command line built
// anywhere in the path.
func TestRunGitUsesArgvExecutorDirectly(t *testing.T) {
	fake := &argvOnlyEnv{result: ExecResult{Stdout: "ok", ExitCode: 0}}
	res, err := RunGit(context.Background(), fake, "/work/dir", 1234, "status", "--porcelain")
	if err != nil {
		t.Fatalf("RunGit: %v", err)
	}
	if res.Stdout != "ok" {
		t.Fatalf("RunGit result = %+v", res)
	}
	if fake.shellCalls != 0 {
		t.Fatalf("RunGit fell back to the shell wrapper: shellCalls = %d", fake.shellCalls)
	}
	if fake.gotName != "git" {
		t.Fatalf("ExecArgv name = %q, want %q", fake.gotName, "git")
	}
	if len(fake.gotArgs) != 2 || fake.gotArgs[0] != "status" || fake.gotArgs[1] != "--porcelain" {
		t.Fatalf("ExecArgv args = %v, want [status --porcelain]", fake.gotArgs)
	}
	if fake.gotTimeoutMS != 1234 {
		t.Fatalf("ExecArgv timeoutMS = %d, want 1234", fake.gotTimeoutMS)
	}
	if fake.gotWorkingDir != "/work/dir" {
		t.Fatalf("ExecArgv workingDir = %q, want %q", fake.gotWorkingDir, "/work/dir")
	}
}

// TestRunGitFallsBackToShellWithoutArgvExecutor proves environments that
// don't implement ArgvExecutor (test fakes, non-local environments) still
// work: RunGit falls back to ExecCommand with the args shell-escaped into a
// single command line, exactly as every git call site did before RunGit
// existed.
func TestRunGitFallsBackToShellWithoutArgvExecutor(t *testing.T) {
	fake := &shellOnlyEnv{result: ExecResult{Stdout: "ok", ExitCode: 0}}
	res, err := RunGit(context.Background(), fake, "/work/dir", 1234, "commit", "-m", "a message with spaces")
	if err != nil {
		t.Fatalf("RunGit: %v", err)
	}
	if res.Stdout != "ok" {
		t.Fatalf("RunGit result = %+v", res)
	}
	want := "git " + shellEscapeArgs("commit", "-m", "a message with spaces")
	if fake.gotCommand != want {
		t.Fatalf("ExecCommand command = %q, want %q", fake.gotCommand, want)
	}
	if fake.gotTimeoutMS != 1234 || fake.gotWorkingDir != "/work/dir" {
		t.Fatalf("ExecCommand timeout/workingDir = %d/%q, want 1234//work/dir", fake.gotTimeoutMS, fake.gotWorkingDir)
	}
}

// TestRunGitAgainstRealGit exercises RunGit against a real LocalExecutionEnvironment
// (which implements ArgvExecutor) to prove the direct-exec path returns the
// same result shape a real git invocation is expected to: stdout trimmed of
// nothing extra, exit code 0, no error, for a real `git rev-parse
// --is-inside-work-tree` in a real repo.
func TestRunGitAgainstRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	env := NewLocalExecutionEnvironment(dir)
	res, err := RunGit(context.Background(), env, dir, 5000, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("RunGit: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("RunGit exit code = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
}

type argvOnlyEnv struct {
	result        ExecResult
	shellCalls    int
	gotName       string
	gotArgs       []string
	gotTimeoutMS  int
	gotWorkingDir string
}

func (e *argvOnlyEnv) Initialize() error                              { return nil }
func (e *argvOnlyEnv) Cleanup()                                       {}
func (e *argvOnlyEnv) WorkingDirectory() string                       { return "" }
func (e *argvOnlyEnv) Platform() string                               { return "test" }
func (e *argvOnlyEnv) OSVersion() string                              { return "test" }
func (e *argvOnlyEnv) FileExists(string) bool                         { return false }
func (e *argvOnlyEnv) Glob(string, string, ...bool) ([]string, error) { return nil, nil }
func (e *argvOnlyEnv) Grep(string, string, string, bool, int, string, ...int) (string, error) {
	return "", nil
}
func (e *argvOnlyEnv) ListDirectory(string, int) ([]DirEntry, error) { return nil, nil }
func (e *argvOnlyEnv) ReadFile(string, *int, *int) (string, error)   { return "", nil }
func (e *argvOnlyEnv) WriteFile(string, string) (string, error)      { return "", nil }
func (e *argvOnlyEnv) EditFile(string, string, string, bool) (string, error) {
	return "", nil
}

func (e *argvOnlyEnv) ExecCommand(context.Context, string, int, string, map[string]string) (ExecResult, error) {
	e.shellCalls++
	return ExecResult{}, nil
}

func (e *argvOnlyEnv) ExecArgv(_ context.Context, name string, args []string, timeoutMS int, workingDir string, _ map[string]string) (ExecResult, error) {
	e.gotName = name
	e.gotArgs = args
	e.gotTimeoutMS = timeoutMS
	e.gotWorkingDir = workingDir
	return e.result, nil
}

type shellOnlyEnv struct {
	result        ExecResult
	gotCommand    string
	gotTimeoutMS  int
	gotWorkingDir string
}

func (e *shellOnlyEnv) Initialize() error                              { return nil }
func (e *shellOnlyEnv) Cleanup()                                       {}
func (e *shellOnlyEnv) WorkingDirectory() string                       { return "" }
func (e *shellOnlyEnv) Platform() string                               { return "test" }
func (e *shellOnlyEnv) OSVersion() string                              { return "test" }
func (e *shellOnlyEnv) FileExists(string) bool                         { return false }
func (e *shellOnlyEnv) Glob(string, string, ...bool) ([]string, error) { return nil, nil }
func (e *shellOnlyEnv) Grep(string, string, string, bool, int, string, ...int) (string, error) {
	return "", nil
}
func (e *shellOnlyEnv) ListDirectory(string, int) ([]DirEntry, error) { return nil, nil }
func (e *shellOnlyEnv) ReadFile(string, *int, *int) (string, error)   { return "", nil }
func (e *shellOnlyEnv) WriteFile(string, string) (string, error)      { return "", nil }
func (e *shellOnlyEnv) EditFile(string, string, string, bool) (string, error) {
	return "", nil
}

func (e *shellOnlyEnv) ExecCommand(_ context.Context, command string, timeoutMS int, workingDir string, _ map[string]string) (ExecResult, error) {
	e.gotCommand = command
	e.gotTimeoutMS = timeoutMS
	e.gotWorkingDir = workingDir
	return e.result, nil
}
