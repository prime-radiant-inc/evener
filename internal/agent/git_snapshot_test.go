package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestGitOriginURL_ReturnsOrigin(t *testing.T) {
	dir := t.TempDir()

	// Resolve symlinks (macOS /var -> /private/var).
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	env := NewLocalExecutionEnvironment(dir)

	// Init a repo and set an origin.
	mustExec(t, env, dir, "git init")
	mustExec(t, env, dir, "git remote add origin https://github.com/example/repo.git")

	got := gitOriginURL(env, dir)
	if got != "https://github.com/example/repo.git" {
		t.Fatalf("gitOriginURL: got %q, want %q", got, "https://github.com/example/repo.git")
	}
}

func TestGitOriginURL_NoOrigin(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	env := NewLocalExecutionEnvironment(dir)

	mustExec(t, env, dir, "git init")

	got := gitOriginURL(env, dir)
	if got != "" {
		t.Fatalf("gitOriginURL: got %q, want empty", got)
	}
}

func TestGitOriginURL_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	env := NewLocalExecutionEnvironment(dir)

	got := gitOriginURL(env, dir)
	if got != "" {
		t.Fatalf("gitOriginURL: got %q, want empty", got)
	}
}

func TestGitOriginURL_NilEnv(t *testing.T) {
	got := gitOriginURL(nil, "/tmp/whatever")
	if got != "" {
		t.Fatalf("gitOriginURL(nil): got %q, want empty", got)
	}
}

// mustExec runs a git command in dir and fails the test on error.
func mustExec(t *testing.T, env ExecutionEnvironment, dir, cmd string) {
	t.Helper()
	// Set HOME to temp dir so git doesn't try to read user config.
	t.Setenv("HOME", t.TempDir())

	res, err := env.ExecCommand(context.Background(), cmd, 5000, dir, nil)
	if err != nil {
		t.Fatalf("exec %q: %v", cmd, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exec %q: exit %d: %s%s", cmd, res.ExitCode, res.Stdout, res.Stderr)
	}
}
