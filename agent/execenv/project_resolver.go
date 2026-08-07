//go:build darwin || linux

package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"primeradiant.com/serf/identifier"
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
		if statErr == nil {
			if info.IsDir() {
				return resolveClean(dir), true, true, nil
			}
			content, readErr := os.ReadFile(gitPath)
			if readErr != nil {
				return "", true, true, fmt.Errorf("read Git pointer %q: %w", gitPath, readErr)
			}
			contentString := string(content)
			if root, ok := identifier.MainRootFromGitdirPointer(contentString, dir); ok {
				if err := validateLinkedPointer(contentString, dir, root); err != nil {
					return "", true, true, err
				}
				return resolveClean(root), true, true, nil
			}
			if _, ok := identifier.ParseGitdirPointer(contentString); !ok {
				return "", true, true, errors.New("malformed Git worktree pointer")
			}
			if err := validateSubmodulePointer(contentString, dir); err != nil {
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

func validateLinkedPointer(content, ancestor, root string) error {
	target, ok := pointerTarget(content, ancestor)
	if !ok {
		return errors.New("malformed Git worktree pointer")
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("linked worktree Git directory %q: %w", target, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("linked worktree Git directory %q is not a directory", target)
	}
	mainGit := filepath.Join(root, ".git")
	mainInfo, err := os.Stat(mainGit)
	if err != nil {
		return fmt.Errorf("main checkout Git directory %q: %w", mainGit, err)
	}
	common := filepath.Dir(filepath.Dir(target))
	if !mainInfo.IsDir() || resolveClean(mainGit) != resolveClean(common) {
		return fmt.Errorf("linked worktree Git directory %q does not match main checkout %q", target, mainGit)
	}
	return nil
}

func validateSubmodulePointer(content, ancestor string) error {
	target, ok := pointerTarget(content, ancestor)
	if !ok {
		return errors.New("malformed Git pointer")
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("git pointer target %q: %w", target, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("git pointer target %q is not a directory", target)
	}
	if !isSubmoduleGitDirShape(target) {
		return fmt.Errorf("git pointer target %q is not a submodule Git directory", target)
	}
	return nil
}

func pointerTarget(content, ancestor string) (string, bool) {
	gitdir, ok := identifier.ParseGitdirPointer(content)
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(ancestor, gitdir)
	}
	return filepath.Clean(gitdir), true
}

func isSubmoduleGitDirShape(target string) bool {
	for current := filepath.Clean(target); ; current = filepath.Dir(current) {
		parent := filepath.Dir(current)
		if filepath.Base(parent) == "modules" && filepath.Base(filepath.Dir(parent)) == ".git" {
			return true
		}
		if parent == current {
			return false
		}
	}
}

func gitBinaryMainRootStrict(env ExecutionEnvironment, cwd string) (string, bool, error) {
	common, err := execGitOutput(env, cwd, "git rev-parse --git-common-dir")
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

	top, err := execGitOutput(env, cwd, "git rev-parse --show-toplevel")
	if err != nil {
		return "", true, fmt.Errorf("resolve Git checkout root: %w", err)
	}
	if !pathContains(top, cwd, isLocalEnv(env)) {
		return "", true, fmt.Errorf("git checkout root %q does not contain %q", top, cwd)
	}
	if isSubmoduleGitDirShape(commonPath) {
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

func execGitOutput(env ExecutionEnvironment, cwd, command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
	defer cancel()
	result, err := env.ExecCommand(ctx, command, gitExecTimeoutMS(), cwd, nil)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("command exited with code %d", result.ExitCode)
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
		root = resolveClean(root)
		path = resolveClean(path)
	}
	root, path = filepath.Clean(root), filepath.Clean(path)
	return root == path || strings.HasPrefix(path, root+string(filepath.Separator))
}

func resolveCleanForEnv(env ExecutionEnvironment, path string) string {
	if isLocalEnv(env) {
		return resolveClean(path)
	}
	return filepath.Clean(path)
}
