package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

type snapshotCountingEnv struct {
	root             string
	execCalls        int
	gitSnapshotCalls int
	commands         []string
}

func (e *snapshotCountingEnv) Initialize() error        { return nil }
func (e *snapshotCountingEnv) Cleanup()                 {}
func (e *snapshotCountingEnv) WorkingDirectory() string { return e.root }
func (e *snapshotCountingEnv) Platform() string         { return "linux" }
func (e *snapshotCountingEnv) OSVersion() string        { return "test" }
func (e *snapshotCountingEnv) ReadFile(string, *int, *int) (string, error) {
	return "", nil
}
func (e *snapshotCountingEnv) WriteFile(string, string) (string, error) {
	return "", nil
}
func (e *snapshotCountingEnv) EditFile(string, string, string, bool) (string, error) {
	return "", nil
}
func (e *snapshotCountingEnv) FileExists(string) bool { return false }
func (e *snapshotCountingEnv) Glob(string, string, ...bool) ([]string, error) {
	return nil, nil
}
func (e *snapshotCountingEnv) Grep(string, string, string, bool, int, string, ...int) (string, error) {
	return "", nil
}
func (e *snapshotCountingEnv) ListDirectory(string, int) ([]execenv.DirEntry, error) {
	return nil, nil
}
func (e *snapshotCountingEnv) ExecCommand(_ context.Context, command string, _ int, _ string, _ map[string]string) (execenv.ExecResult, error) {
	e.execCalls++
	e.commands = append(e.commands, command)
	if isGitSnapshotCommand(command) {
		e.gitSnapshotCalls++
	}
	return execenv.ExecResult{ExitCode: 1}, nil
}

func isGitSnapshotCommand(command string) bool {
	switch command {
	case "git rev-parse --is-inside-work-tree",
		"git rev-parse --abbrev-ref HEAD",
		"git status --porcelain",
		"git log -n 5 --pretty=format:%h%x20%s",
		"git remote get-url origin":
		return true
	default:
		return false
	}
}

func TestSnapshotGit_NonRepoDoesNotShellOut(t *testing.T) {
	dir := t.TempDir()
	env := &snapshotCountingEnv{root: dir}

	inRepo, branch, modified, untracked, commits := snapshotGit(env, dir)
	if inRepo {
		t.Fatal("expected inRepo=false for non-git directory")
	}
	if branch != "" || modified != 0 || untracked != 0 || len(commits) != 0 {
		t.Fatalf("snapshotGit returned repository data for non-git directory: branch=%q modified=%d untracked=%d commits=%v", branch, modified, untracked, commits)
	}
	if env.execCalls != 0 {
		t.Fatalf("ExecCommand calls = %d, want 0 for non-git directory", env.execCalls)
	}
}

func TestNewSession_TestConfigCanSkipGitSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	env := &snapshotCountingEnv{root: dir}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		testOnly: testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	if env.gitSnapshotCalls != 0 {
		t.Fatalf("git snapshot calls = %d, want 0 when git snapshot is skipped; commands=%v", env.gitSnapshotCalls, env.commands)
	}
	if sess.envInfo.IsGitRepo {
		t.Fatal("envInfo.IsGitRepo = true, want false when git snapshot is skipped")
	}
}

func TestGitOriginURL_ReturnsOrigin(t *testing.T) {
	dir := t.TempDir()

	// Resolve symlinks (macOS /var -> /private/var).
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	env := execenv.NewLocalExecutionEnvironment(dir)

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
	env := execenv.NewLocalExecutionEnvironment(dir)

	mustExec(t, env, dir, "git init")

	got := gitOriginURL(env, dir)
	if got != "" {
		t.Fatalf("gitOriginURL: got %q, want empty", got)
	}
}

func TestGitOriginURL_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	env := execenv.NewLocalExecutionEnvironment(dir)

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
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
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
	env := execenv.NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	inRepo, _, _, _, _ := snapshotGit(env, dir)
	if inRepo {
		t.Fatal("expected inRepo=false for non-git directory")
	}
}

func TestSnapshotGit_FreshRepoNoCommits(t *testing.T) {
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
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
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
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
func mustExec(t *testing.T, env execenv.ExecutionEnvironment, dir, cmd string) {
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
