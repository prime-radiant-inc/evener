package agent

import (
	"context"
	"os"
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

func TestSnapshotGit_InGitRepo(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	if _, err := env.ExecCommand(ctx, "git init", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := env.ExecCommand(ctx, "git config user.email test@test.com", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := env.ExecCommand(ctx, "git config user.name test", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := env.ExecCommand(ctx, "git add f.txt && git commit -m initial", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}

	inRepo, branch, mod, untracked, commits := snapshotGit(env, dir)
	if !inRepo {
		t.Fatal("expected inRepo=true")
	}
	if branch == "" {
		t.Fatal("expected non-empty branch")
	}
	if len(commits) == 0 {
		t.Fatal("expected at least 1 commit")
	}
	if mod != 0 {
		t.Errorf("expected 0 modified files, got %d", mod)
	}
	if untracked != 0 {
		t.Errorf("expected 0 untracked files, got %d", untracked)
	}
}

func TestSnapshotGit_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	inRepo, _, _, _, _ := snapshotGit(env, dir)
	if inRepo {
		t.Fatal("expected inRepo=false for non-git directory")
	}
}

func TestSnapshotGit_FreshRepoNoCommits(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	if _, err := env.ExecCommand(ctx, "git init", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}

	inRepo, _, _, _, commits := snapshotGit(env, dir)
	if !inRepo {
		t.Fatal("expected inRepo=true")
	}
	if len(commits) != 0 {
		t.Errorf("expected 0 commits for fresh repo, got %d", len(commits))
	}
}

func TestSnapshotGit_TracksModifiedAndUntracked(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	if _, err := env.ExecCommand(ctx, "git init", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := env.ExecCommand(ctx, "git config user.email test@test.com", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := env.ExecCommand(ctx, "git config user.name test", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := env.ExecCommand(ctx, "git add tracked.txt && git commit -m initial", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, mod, untracked, _ := snapshotGit(env, dir)
	if mod != 1 {
		t.Errorf("expected 1 modified, got %d", mod)
	}
	if untracked != 1 {
		t.Errorf("expected 1 untracked, got %d", untracked)
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
