package execenv

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"primeradiant.com/evener/identifier"
)

// NewProjectResolver binds identifier's project policy to an execution
// environment. The identifier package remains the policy owner; this adapter
// only supplies path and Git operations through env.
func NewProjectResolver(env ExecutionEnvironment) identifier.Resolver {
	if isNilExecutionEnvironment(env) {
		return (*projectResolver)(nil)
	}
	return &projectResolver{env: env}
}

type projectResolver struct{ env ExecutionEnvironment }

func isNilExecutionEnvironment(env ExecutionEnvironment) bool {
	if env == nil {
		return true
	}
	value := reflect.ValueOf(env)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (r *projectResolver) Abs(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	base := r.env.WorkingDirectory()
	if base == "" {
		return "", errors.New("resolve project relative path: execution environment working directory is empty")
	}
	absolute, err := filepath.Abs(filepath.Join(base, path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func (r *projectResolver) EvalSymlinks(path string) (string, error) {
	if _, local := r.env.(*LocalExecutionEnvironment); local {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%q is not a directory", resolved)
		}
		return resolved, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
	defer cancel()
	result, err := r.env.ExecCommand(ctx, "pwd -P", gitExecTimeoutMS(), path, nil)
	if err != nil {
		return "", fmt.Errorf("evaluate symlinks with pwd -P: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("pwd -P exited with code %d", result.ExitCode)
	}
	resolved := strings.TrimSpace(result.Stdout)
	if resolved == "" {
		return "", errors.New("pwd -P returned blank output")
	}
	return filepath.Clean(resolved), nil
}

func (r *projectResolver) MainCheckout(path string) (string, bool, error) {
	return resolveMainRepoRoot(r.env, path)
}

// resolveMainRepoRoot is the strict execution-environment boundary used by the
// identifier adapter. A missing .git entry is the only non-Git success. Once a
// Git entry is observed, every malformed, incomplete, or contradictory result
// remains Git and returns an error instead of silently changing identity.
func resolveMainRepoRoot(env ExecutionEnvironment, cwd string) (root string, isGit bool, err error) {
	if _, local := env.(*LocalExecutionEnvironment); local {
		if root, detected, handled, err := localStructuralMainRoot(cwd); handled {
			return root, detected, err
		}
	}
	return gitBinaryMainRootStrict(env, cwd)
}

func envHasGitEntryAncestor(env ExecutionEnvironment, cwd string) bool {
	dir := filepath.Clean(cwd)
	for {
		if env.FileExists(filepath.Join(dir, ".git")) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func localStructuralMainRoot(cwd string) (root string, isGit bool, handled bool, err error) {
	dir := filepath.Clean(cwd)
	for {
		gitPath := filepath.Join(dir, ".git")
		info, statErr := os.Stat(gitPath)
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			// Only an absence the filesystem actually reported lets the walk
			// move on: a stat that failed for want of permission (or on a
			// filesystem error) leaves it unknown whether a .git is there. Say
			// so rather than walking past it, or the caller memoizes an absence
			// nothing ever observed — the walk shares gitRootCache with
			// GitRootOrEmpty, and a cached "not a repository" outlives the
			// condition that produced it. This error is not an exit status, so
			// mainRepoRootUncached's classification treats it as transient.
			return "", false, true, fmt.Errorf("stat %q: %w", gitPath, statErr)
		}
		if statErr == nil {
			if info.IsDir() {
				return identifier.ResolveClean(dir), true, true, nil
			}
			content, readErr := os.ReadFile(gitPath)
			if readErr != nil {
				return "", true, true, fmt.Errorf("read Git pointer %q: %w", gitPath, readErr)
			}
			contentString := string(content)
			if root, ok := identifier.MainRootFromGitdirPointer(contentString, dir); ok {
				if err := identifier.ValidateLinkedWorktreePointer(contentString, dir, root); err != nil {
					return "", true, true, err
				}
				return identifier.ResolveClean(root), true, true, nil
			}
			if _, ok := identifier.ParseGitdirPointer(contentString); !ok {
				return "", true, true, errors.New("malformed Git worktree pointer")
			}
			if err := identifier.ValidateSubmodulePointer(contentString, dir); err != nil {
				return "", true, true, err
			}
			return "", true, false, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, true, nil
		}
		dir = parent
	}
}

func gitBinaryMainRootStrict(env ExecutionEnvironment, cwd string) (string, bool, error) {
	common, err := execGitOutput(env, cwd, "rev-parse", "--git-common-dir")
	if err != nil {
		if !isLocalEnv(env) && !envHasGitEntryAncestor(env, cwd) {
			return "", false, nil
		}
		return "", true, fmt.Errorf("resolve Git common directory: %w", err)
	}
	candidate := identifier.MainRootCandidateFromCommonDir(cwd, common)
	if candidate != "" && isLocalEnv(env) && identifier.GitEntryResolvesToCommon(candidate, common) {
		return candidate, true, nil
	}
	commonPath := common
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(cwd, commonPath)
	}
	commonPath = filepath.Clean(commonPath)

	top, err := execGitOutput(env, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", true, fmt.Errorf("resolve Git checkout root: %w", err)
	}
	if !pathContains(top, cwd, isLocalEnv(env)) {
		return "", true, fmt.Errorf("git checkout root %q does not contain %q", top, cwd)
	}
	if identifier.IsSubmoduleGitDirShape(commonPath) {
		if !isLocalEnv(env) && !env.FileExists(commonPath) {
			return "", true, fmt.Errorf("git submodule common directory %q does not exist", commonPath)
		}
		return resolveCleanForEnv(env, top), true, nil
	}
	if !isLocalEnv(env) {
		if candidate == "" || !env.FileExists(filepath.Join(candidate, ".git")) || commonPath != filepath.Join(filepath.Clean(candidate), ".git") {
			return "", true, fmt.Errorf("git common directory %q does not identify a validated checkout root", common)
		}
		return filepath.Clean(candidate), true, nil
	}
	if candidate == "" || !pathContains(candidate, top, true) {
		return "", true, fmt.Errorf("git common directory %q does not identify checkout root %q", common, top)
	}
	return resolveCleanForEnv(env, candidate), true, nil
}

func execGitOutput(env ExecutionEnvironment, cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
	defer cancel()
	result, err := RunGit(ctx, env, cwd, gitExecTimeoutMS(), args...)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("command exited with code %d", result.ExitCode)
	}
	// A truncated STDOUT capture is head and tail fragments; a partial path
	// decoded here would be a wrong answer, so refuse it (#4568 round 2). The
	// answer comes from stdout, so a capped STDERR still leaves it whole
	// (#4621 round 16).
	if result.StdoutTruncated {
		return "", ErrOutputTruncated
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		return "", errors.New("command returned blank output")
	}
	return output, nil
}

func isLocalEnv(env ExecutionEnvironment) bool {
	_, ok := env.(*LocalExecutionEnvironment)
	return ok
}

func pathContains(root, path string, resolveSymlinks bool) bool {
	if resolveSymlinks {
		root = identifier.ResolveClean(root)
		path = identifier.ResolveClean(path)
	}
	return identifier.PathUnder(path, root)
}

func resolveCleanForEnv(env ExecutionEnvironment, path string) string {
	if isLocalEnv(env) {
		return identifier.ResolveClean(path)
	}
	return filepath.Clean(path)
}
