package execenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
)

// TestProjectResolver_Abs_AbsolutePath covers the absolute-path fast path
// in Abs (line 41-42).
func TestProjectResolver_Abs_AbsolutePath(t *testing.T) {
	env := &fakeExecEnv{workDir: t.TempDir()}
	r := NewProjectResolver(env)
	got, err := r.Abs("/foo/bar")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if got != "/foo/bar" {
		t.Fatalf("Abs = %q, want /foo/bar", got)
	}
}

// TestProjectResolver_Abs_RelativePath covers the relative-path resolution
// in Abs (lines 44-52).
func TestProjectResolver_Abs_RelativePath(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{workDir: dir}
	r := NewProjectResolver(env)
	got, err := r.Abs("subdir")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	want := filepath.Join(dir, "subdir")
	if got != want {
		t.Fatalf("Abs = %q, want %q", got, want)
	}
}

// TestProjectResolver_Abs_EmptyWorkDir covers the empty working-directory
// error in Abs (line 45-46).
func TestProjectResolver_Abs_EmptyWorkDir(t *testing.T) {
	env := &fakeExecEnv{workDir: ""}
	r := NewProjectResolver(env)
	_, err := r.Abs("relative")
	if err == nil || !strings.Contains(err.Error(), "working directory is empty") {
		t.Fatalf("Abs with empty workDir: %v, want empty working directory error", err)
	}
}

// TestProjectResolver_EvalSymlinks_LocalNotDir covers the EvalSymlinks path
// where a resolved path is not a directory (line 65-66).
func TestProjectResolver_EvalSymlinks_LocalNotDir(t *testing.T) {
	dir := t.TempDir()
	// Create a regular file.
	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(dir)
	r := NewProjectResolver(env)
	_, err := r.EvalSymlinks(filePath)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("EvalSymlinks on file: %v, want 'not a directory'", err)
	}
}

// TestProjectResolver_EvalSymlinks_NonExistent covers the EvalSymlinks error
// path for a non-existent path (line 58-59).
func TestProjectResolver_EvalSymlinks_NonExistent(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	r := NewProjectResolver(env)
	_, err := r.EvalSymlinks(filepath.Join(t.TempDir(), "nonexistent"))
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

// TestProjectResolver_EvalSymlinks_NonLocal covers the EvalSymlinks non-local
// path via pwd -P (lines 70-83).
func TestProjectResolver_EvalSymlinks_NonLocal(t *testing.T) {
	env := &fakeExecEnv{
		workDir: t.TempDir(),
		exec: func(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
			if strings.Contains(command, "pwd -P") {
				return ExecResult{ExitCode: 0, Stdout: t.TempDir() + "\n"}, nil
			}
			return ExecResult{ExitCode: 1}, nil
		},
	}
	r := NewProjectResolver(env)
	got, err := r.EvalSymlinks(".")
	if err != nil {
		t.Fatalf("EvalSymlinks non-local: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty result")
	}
}

// TestProjectResolver_EvalSymlinks_NonLocal_ExecError covers the non-local
// EvalSymlinks path where ExecCommand returns an error (line 73-74).
func TestProjectResolver_EvalSymlinks_NonLocal_ExecError(t *testing.T) {
	env := &fakeExecEnv{
		workDir: t.TempDir(),
		exec: func(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
			return ExecResult{}, context.DeadlineExceeded
		},
	}
	r := NewProjectResolver(env)
	_, err := r.EvalSymlinks(".")
	if err == nil {
		t.Fatal("expected error for exec failure")
	}
}

// TestProjectResolver_EvalSymlinks_NonLocal_NonZeroExit covers the non-local
// EvalSymlinks path where pwd -P exits non-zero (line 76-77).
func TestProjectResolver_EvalSymlinks_NonLocal_NonZeroExit(t *testing.T) {
	env := &fakeExecEnv{
		workDir: t.TempDir(),
		exec: func(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
			return ExecResult{ExitCode: 1}, nil
		},
	}
	r := NewProjectResolver(env)
	_, err := r.EvalSymlinks(".")
	if err == nil || !strings.Contains(err.Error(), "exited with code") {
		t.Fatalf("error = %v, want exited with code", err)
	}
}

// TestProjectResolver_EvalSymlinks_NonLocal_BlankOutput covers the non-local
// EvalSymlinks path where pwd -P returns blank output (line 79-81).
func TestProjectResolver_EvalSymlinks_NonLocal_BlankOutput(t *testing.T) {
	env := &fakeExecEnv{
		workDir: t.TempDir(),
		exec: func(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
			return ExecResult{ExitCode: 0, Stdout: ""}, nil
		},
	}
	r := NewProjectResolver(env)
	_, err := r.EvalSymlinks(".")
	if err == nil || !strings.Contains(err.Error(), "blank output") {
		t.Fatalf("error = %v, want blank output error", err)
	}
}

// TestValidateLinkedPointer_Malformed covers the malformed pointer path
// in validateLinkedPointer (line 155-156).
func TestValidateLinkedPointer_Malformed(t *testing.T) {
	err := validateLinkedPointer("not a pointer", "/ancestor", "/root")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error = %v, want malformed", err)
	}
}

// TestValidateLinkedPointer_TargetNotDir covers the non-directory target
// path in validateLinkedPointer (line 162-163).
func TestValidateLinkedPointer_TargetNotDir(t *testing.T) {
	dir := t.TempDir()
	// Create a file, not a directory, as the target.
	target := filepath.Join(dir, "file")
	os.WriteFile(target, []byte("x"), 0o644)
	content := "gitdir: " + target
	err := validateLinkedPointer(content, dir, dir)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want not a directory", err)
	}
}

// TestValidateSubmodulePointer_Malformed covers the malformed pointer path
// in validateSubmodulePointer (line 179-180).
func TestValidateSubmodulePointer_Malformed(t *testing.T) {
	err := validateSubmodulePointer("not a pointer", "/ancestor")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error = %v, want malformed", err)
	}
}

// TestValidateSubmodulePointer_NotDir covers the non-directory target path
// in validateSubmodulePointer (line 186-187).
func TestValidateSubmodulePointer_NotDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file")
	os.WriteFile(target, []byte("x"), 0o644)
	content := "gitdir: " + target
	err := validateSubmodulePointer(content, dir)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want not a directory", err)
	}
}

// TestValidateSubmodulePointer_NotSubmoduleShape covers the not-submodule
// shape path in validateSubmodulePointer (line 189-190).
func TestValidateSubmodulePointer_NotSubmoduleShape(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gitdir")
	os.MkdirAll(target, 0o755)
	content := "gitdir: " + target
	err := validateSubmodulePointer(content, dir)
	if err == nil || !strings.Contains(err.Error(), "not a submodule") {
		t.Fatalf("error = %v, want not a submodule", err)
	}
}

// TestResolveMainRepoRoot_NonGitDir covers the non-Git directory path
// in resolveMainRepoRoot (line 96-97, 100).
func TestResolveMainRepoRoot_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	// No .git entry anywhere.
	root, isGit, err := resolveMainRepoRoot(NewLocalExecutionEnvironment(dir), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isGit {
		t.Fatal("expected isGit=false for non-Git directory")
	}
	if root != "" {
		t.Fatalf("root = %q, want empty", root)
	}
}

// TestResolveMainRepoRoot_GitDir covers the Git directory path where
// .git is a directory (line 123-124).
func TestResolveMainRepoRoot_GitDir(t *testing.T) {
	dir := t.TempDir()
	// Create .git as a directory.
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	root, isGit, err := resolveMainRepoRoot(NewLocalExecutionEnvironment(dir), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isGit {
		t.Fatal("expected isGit=true for Git directory")
	}
	if root == "" {
		t.Fatal("expected non-empty root")
	}
}

// TestResolveMainRepoRoot_MalformedGitdirPointer covers the path where .git
// is a file with a malformed pointer (line 137-138).
func TestResolveMainRepoRoot_MalformedGitdirPointer(t *testing.T) {
	dir := t.TempDir()
	// Create .git as a file with a non-gitdir pointer.
	os.WriteFile(filepath.Join(dir, ".git"), []byte("garbage"), 0o644)
	_, isGit, err := resolveMainRepoRoot(NewLocalExecutionEnvironment(dir), dir)
	if err == nil {
		t.Fatal("expected error for malformed gitdir pointer")
	}
	if !isGit {
		t.Fatal("expected isGit=true for malformed gitdir (Git entry observed)")
	}
}

// TestGitBinaryMainRootStrict_ExecError covers the exec error path in
// gitBinaryMainRootStrict (line 220-224) using a fake env with a Git entry
// ancestor so the non-local branch returns an error.
func TestGitBinaryMainRootStrict_ExecError(t *testing.T) {
	dir := t.TempDir()
	env := &fakeExecEnv{
		workDir: dir,
		exists: func(path string) bool {
			return path == filepath.Join(dir, ".git")
		},
		exec: func(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
			return ExecResult{}, context.DeadlineExceeded
		},
	}
	_, isGit, err := gitBinaryMainRootStrict(env, dir)
	if err == nil {
		t.Fatal("expected error for exec failure")
	}
	if !isGit {
		t.Fatal("expected isGit=true for env with Git entry ancestor")
	}
}

// TestEnvHasGitEntryAncestor covers the envHasGitEntryAncestor function
// (lines 103-114).
func TestEnvHasGitEntryAncestor(t *testing.T) {
	dir := t.TempDir()
	// Create a .git entry.
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	env := &fakeExecEnv{
		workDir: dir,
		exists: func(path string) bool {
			return path == filepath.Join(dir, ".git")
		},
	}
	if !envHasGitEntryAncestor(env, dir) {
		t.Fatal("expected Git entry ancestor")
	}

	// No .git — should return false.
	env2 := &fakeExecEnv{
		workDir: t.TempDir(),
		exists:  func(path string) bool { return false },
	}
	if envHasGitEntryAncestor(env2, t.TempDir()) {
		t.Fatal("expected no Git entry ancestor")
	}
}

// TestProjectResolver_MainCheckout_NonGit covers the MainCheckout path
// for a non-Git directory.
func TestProjectResolver_MainCheckout_NonGit(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	r := NewProjectResolver(env)
	root, isGit, err := r.MainCheckout(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isGit || root != "" {
		t.Fatalf("root = %q isGit = %v, want empty and false", root, isGit)
	}
}

// TestIdentifierResolveProjectWith covers the full resolution path using
// identifier.ResolveProjectWith.
func TestProjectResolver_ResolveProjectWith_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	_, err := identifier.ResolveProjectWith(dir, NewProjectResolver(env))
	if err != nil {
		t.Fatalf("ResolveProjectWith: %v", err)
	}
}
