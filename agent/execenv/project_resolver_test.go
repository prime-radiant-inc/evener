package execenv

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
)

func TestProjectResolver_NilEnvironment(t *testing.T) {
	for name, env := range map[string]ExecutionEnvironment{
		"nil interface": nil,
		"typed nil":     (*fakeExecEnv)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("panicked: %v", recovered)
				}
			}()
			if _, err := identifier.ResolveProjectWith(filepath.Join(t.TempDir(), "project"), NewProjectResolver(env)); err == nil {
				t.Fatal("nil environment accepted")
			}
		})
	}
	if reflect.TypeOf(NewProjectResolver(nil)).Kind() != reflect.Pointer {
		t.Fatal("nil environment resolver is not a pointer nil-resolver")
	}
}

func TestProjectResolver_EmptyEnvironmentWorkingDirectoryRejectsRelative(t *testing.T) {
	env := &fakeExecEnv{workDir: "", exec: func(context.Context, string, int, string, map[string]string) (ExecResult, error) {
		return ExecResult{}, nil
	}}
	_, err := identifier.ResolveProjectWith("relative", NewProjectResolver(env))
	if err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("relative path with empty environment cwd error = %v, want contextual error", err)
	}
}

func TestProjectResolver_GenericLinkedWorktreeUsesMainRoot(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	worktree := filepath.Join(base, "worktree")
	cwd := filepath.Join(worktree, "nested")
	common := filepath.Join(main, ".git")
	if err := os.MkdirAll(filepath.Join(main, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := []string{}
	env := &fakeExecEnv{workDir: cwd, exists: func(path string) bool {
		return path == filepath.Join(main, ".git")
	}, exec: func(_ context.Context, command string, _ int, workingDir string, _ map[string]string) (ExecResult, error) {
		commands = append(commands, command+" @ "+workingDir)
		switch command {
		case "pwd -P":
			return ExecResult{Stdout: workingDir + "\n", ExitCode: 0}, nil
		case "git rev-parse --git-common-dir":
			return ExecResult{Stdout: common + "\n", ExitCode: 0}, nil
		case "git rev-parse --show-toplevel":
			return ExecResult{Stdout: worktree + "\n", ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command %q", command)
			return ExecResult{}, nil
		}
	}}
	project, err := identifier.ResolveProjectWith(".", NewProjectResolver(env))
	if err != nil {
		t.Fatal(err)
	}
	if project.CanonicalPath != main {
		t.Fatalf("canonical path = %q, want %q", project.CanonicalPath, main)
	}
	if len(commands) != 4 || commands[0] != "pwd -P @ "+cwd || commands[1] != "git rev-parse --git-common-dir @ "+cwd || commands[2] != "git rev-parse --show-toplevel @ "+cwd {
		t.Fatalf("generic command evidence = %v, want pwd, both Git commands, and final main-root pwd", commands)
	}
}

func TestResolveMainRepoRootStrict_GenericDefiniteNonGit(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{workDir: dir, exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
		if command != "git rev-parse --git-common-dir" {
			t.Fatalf("unexpected command %q", command)
		}
		return ExecResult{ExitCode: 128}, nil
	}}
	root, isGit, err := resolveMainRepoRoot(env, dir)
	if root != "" || isGit || err != nil {
		t.Fatalf("generic non-Git = (%q, %v, %v), want (empty, false, nil)", root, isGit, err)
	}
}

func TestResolveMainRepoRootStrict_GenericBogusCandidateRejected(t *testing.T) {
	cwd := t.TempDir()
	bogus := filepath.Join(t.TempDir(), "bogus")
	env := &fakeExecEnv{workDir: cwd, exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
		switch command {
		case "git rev-parse --git-common-dir":
			return ExecResult{Stdout: filepath.Join(bogus, ".git") + "\n", ExitCode: 0}, nil
		case "git rev-parse --show-toplevel":
			return ExecResult{Stdout: cwd + "\n", ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command %q", command)
			return ExecResult{}, nil
		}
	}}
	root, isGit, err := resolveMainRepoRoot(env, cwd)
	if root != "" || !isGit || err == nil {
		t.Fatalf("bogus generic candidate = (%q, %v, %v), want (empty, true, error)", root, isGit, err)
	}
}

func TestResolveMainRepoRootStrict_GenericRelativeCommonDir(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := &fakeExecEnv{workDir: cwd, exists: func(path string) bool {
		return path == filepath.Join(cwd, ".git")
	}, exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
		switch command {
		case "git rev-parse --git-common-dir":
			return ExecResult{Stdout: ".git\n", ExitCode: 0}, nil
		case "git rev-parse --show-toplevel":
			return ExecResult{Stdout: cwd + "\n", ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command %q", command)
			return ExecResult{}, nil
		}
	}}
	root, isGit, err := resolveMainRepoRoot(env, cwd)
	if err != nil || !isGit || root != cwd {
		t.Fatalf("relative common generic = (%q, %v, %v), want (%q, true, nil)", root, isGit, err, cwd)
	}
}

func TestResolveMainRepoRootStrict_GenericSubmoduleCommonDirRequiresExistence(t *testing.T) {
	cases := []struct {
		name      string
		common    string
		exists    bool
		wantRoot  bool
		expectErr bool
	}{
		{name: "missing", common: "/bogus/.git/modules/name", exists: false, expectErr: true},
		{name: "ordinary", common: "/super/.git/modules/name", exists: true, wantRoot: true},
		{name: "nested", common: "/super/.git/modules/outer/modules/inner", exists: true, wantRoot: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd := filepath.Join(t.TempDir(), "worktree")
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			env := &fakeExecEnv{workDir: cwd, exists: func(path string) bool {
				return tc.exists && path == filepath.Clean(tc.common)
			}, exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
				switch command {
				case "git rev-parse --git-common-dir":
					return ExecResult{Stdout: tc.common + "\n", ExitCode: 0}, nil
				case "git rev-parse --show-toplevel":
					return ExecResult{Stdout: filepath.Dir(cwd) + "\n", ExitCode: 0}, nil
				default:
					t.Fatalf("unexpected command %q", command)
					return ExecResult{}, nil
				}
			}}
			root, isGit, err := resolveMainRepoRoot(env, cwd)
			if tc.expectErr {
				if root != "" || !isGit || err == nil {
					t.Fatalf("missing submodule common = (%q, %v, %v), want (empty, true, error)", root, isGit, err)
				}
				return
			}
			if !tc.wantRoot || err != nil || !isGit || root != filepath.Dir(cwd) {
				t.Fatalf("valid submodule common = (%q, %v, %v), want (%q, true, nil)", root, isGit, err, filepath.Dir(cwd))
			}
		})
	}
}

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
