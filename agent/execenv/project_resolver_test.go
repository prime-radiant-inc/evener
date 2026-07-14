package execenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
)

func TestProjectResolver_LocalRepository(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	project, err := identifier.ResolveProjectWith(repo, NewProjectResolver(NewLocalExecutionEnvironment(repo)))
	if err != nil {
		t.Fatal(err)
	}
	if project.CanonicalPath != evalSym(t, repo) {
		t.Fatalf("canonical path = %q, want %q", project.CanonicalPath, evalSym(t, repo))
	}
	if err := identifier.ValidateProjectID(project.ID); err != nil {
		t.Fatalf("project ID %q: %v", project.ID, err)
	}
}

func TestProjectResolver_LinkedWorktreeUsesMainCheckout(t *testing.T) {
	main, worktree := newLinkedWorktree(t)
	project, err := identifier.ResolveProjectWith(worktree, NewProjectResolver(NewLocalExecutionEnvironment(worktree)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := project.CanonicalPath, evalSym(t, main); got != want {
		t.Fatalf("canonical path = %q, want main %q", got, want)
	}
}

func TestProjectResolver_SubmoduleUsesSubmoduleCheckout(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	super := filepath.Join(base, "super")
	runGit(t, base, "init", "-q", "sub")
	runGit(t, sub, "commit", "-q", "--allow-empty", "-m", "seed")
	runGit(t, base, "init", "-q", "super")
	runGit(t, super, "commit", "-q", "--allow-empty", "-m", "seed")
	runGit(t, super, "submodule", "add", "-q", "../sub", "sub")
	worktree := filepath.Join(super, "sub")
	project, err := identifier.ResolveProjectWith(worktree, NewProjectResolver(NewLocalExecutionEnvironment(worktree)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := project.CanonicalPath, evalSym(t, worktree); got != want {
		t.Fatalf("canonical path = %q, want submodule %q", got, want)
	}
}

func TestProjectResolver_NonGitDirectory(t *testing.T) {
	dir := t.TempDir()
	project, err := identifier.ResolveProjectWith(dir, NewProjectResolver(NewLocalExecutionEnvironment(dir)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := project.CanonicalPath, evalSym(t, dir); got != want {
		t.Fatalf("canonical path = %q, want %q", got, want)
	}
}

func TestProjectResolver_NonexistentDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	_, err := identifier.ResolveProjectWith(dir, NewProjectResolver(NewLocalExecutionEnvironment(dir)))
	if err == nil {
		t.Fatal("ResolveProjectWith(nonexistent) error = nil")
	}
}

func TestProjectResolver_GenericGitErrorsAreStrict(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{workDir: dir, exec: func(_ context.Context, command string, _ int, workingDir string, _ map[string]string) (ExecResult, error) {
		if command == "pwd -P" {
			if workingDir != dir {
				t.Fatalf("pwd working directory = %q, want %q", workingDir, dir)
			}
			return ExecResult{Stdout: dir + "\n", ExitCode: 0}, nil
		}
		return ExecResult{ExitCode: 128}, nil
	}, exists: func(path string) bool { return path == filepath.Join(dir, ".git") }}
	_, err := identifier.ResolveProjectWith(dir, NewProjectResolver(env))
	if err == nil || !strings.Contains(err.Error(), "Git checkout") {
		t.Fatalf("strict generic Git error = %v, want checkout error", err)
	}
}

func TestProjectResolver_GenericBlankDirectoryOutputIsStrict(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{workDir: dir, exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
		if command != "pwd -P" {
			t.Fatalf("unexpected command %q", command)
		}
		return ExecResult{Stdout: " \n", ExitCode: 0}, nil
	}}
	_, err := identifier.ResolveProjectWith(dir, NewProjectResolver(env))
	if err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("blank pwd error = %v, want symlink error", err)
	}
}

func TestProjectResolver_GenericContainmentMismatchIsStrict(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	env := &fakeExecEnv{workDir: dir, exec: func(_ context.Context, command string, _ int, workingDir string, _ map[string]string) (ExecResult, error) {
		switch command {
		case "pwd -P":
			return ExecResult{Stdout: dir + "\n", ExitCode: 0}, nil
		case "git rev-parse --git-common-dir":
			return ExecResult{Stdout: filepath.Join(dir, "missing", ".git") + "\n", ExitCode: 0}, nil
		case "git rev-parse --show-toplevel":
			if workingDir != dir {
				t.Fatalf("git working directory = %q, want %q", workingDir, dir)
			}
			return ExecResult{Stdout: elsewhere + "\n", ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command %q", command)
			return ExecResult{}, nil
		}
	}, exists: func(path string) bool { return path == filepath.Join(dir, ".git") }}
	_, err := identifier.ResolveProjectWith(dir, NewProjectResolver(env))
	if err == nil || !strings.Contains(err.Error(), "Git checkout") {
		t.Fatalf("containment mismatch error = %v, want checkout error", err)
	}
}

func TestProjectResolver_MalformedDetectedPointerIsStrict(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a git pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := identifier.ResolveProjectWith(dir, NewProjectResolver(NewLocalExecutionEnvironment(dir)))
	if err == nil || !strings.Contains(err.Error(), "Git checkout") {
		t.Fatalf("malformed pointer error = %v, want checkout error", err)
	}
}

func TestProjectResolver_GenericPathIsNotInterpolated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "path;touch SHOULD_NOT_EXIST")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var gotCommand, gotDir string
	env := &fakeExecEnv{workDir: filepath.Dir(dir), exec: func(_ context.Context, command string, _ int, workingDir string, _ map[string]string) (ExecResult, error) {
		gotCommand, gotDir = command, workingDir
		return ExecResult{Stdout: dir + "\n", ExitCode: 0}, nil
	}}
	if _, err := NewProjectResolver(env).EvalSymlinks(dir); err != nil {
		t.Fatal(err)
	}
	if gotCommand != "pwd -P" || gotDir != dir {
		t.Fatalf("generic pwd invocation = command %q, dir %q", gotCommand, gotDir)
	}
}

func TestResolveMainRepoRootStrict_NonGit(t *testing.T) {
	dir := t.TempDir()
	root, isGit, err := resolveMainRepoRoot(NewLocalExecutionEnvironment(dir), dir)
	if err != nil || isGit || root != "" {
		t.Fatalf("non-Git strict result = (%q, %v, %v), want (empty, false, nil)", root, isGit, err)
	}
}

func TestResolveMainRepoRootStrict_DetectedButUnresolvable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: missing/worktrees/nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, isGit, err := resolveMainRepoRoot(NewLocalExecutionEnvironment(dir), dir)
	if err == nil || !isGit || root != "" {
		t.Fatalf("unresolvable strict result = (%q, %v, %v), want (empty, true, error)", root, isGit, err)
	}
}
