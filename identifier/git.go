package identifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ParseGitdirPointer extracts the first non-empty gitdir pointer value.
func ParseGitdirPointer(content string) (string, bool) {
	for line := range strings.SplitSeq(content, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if ok && strings.TrimSpace(rest) != "" {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// MainRootFromGitdirPointer returns the main checkout root for a linked
// worktree pointer, without filesystem access.
func MainRootFromGitdirPointer(pointerContent, ancestorDir string) (string, bool) {
	gitdir, ok := ParseGitdirPointer(pointerContent)
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(ancestorDir, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	worktreesDir := filepath.Dir(gitdir)
	if filepath.Base(worktreesDir) != "worktrees" {
		return "", false
	}
	root := filepath.Dir(filepath.Dir(worktreesDir))
	if root == "" || root == "." {
		return "", false
	}
	return root, true
}

// MainRootCandidateFromCommonDir turns git-common-dir output into a root.
func MainRootCandidateFromCommonDir(cwd, common string) string {
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	common = filepath.Clean(common)
	root := filepath.Dir(common)
	if root == "" || root == "." {
		return ""
	}
	return root
}

// GitEntryResolvesToCommon checks that candidate's .git entry points to common.
func GitEntryResolvesToCommon(candidate, common string) bool {
	gitPath := filepath.Join(candidate, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	common = resolveClean(common)
	if info.IsDir() {
		return resolveClean(gitPath) == common
	}
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return false
	}
	gitdir, ok := ParseGitdirPointer(string(content))
	if !ok {
		return false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(candidate, gitdir)
	}
	return resolveClean(filepath.Dir(filepath.Dir(gitdir))) == common
}

func resolveClean(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func mainCheckoutLocal(cwd string) (string, bool, error) {
	dir := filepath.Clean(cwd)
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return dir, true, nil
			}
			content, readErr := os.ReadFile(gitPath)
			if readErr != nil {
				return "", true, fmt.Errorf("read Git pointer %q: %w", gitPath, readErr)
			}
			if root, ok := MainRootFromGitdirPointer(string(content), dir); ok {
				if err := validateLinkedWorktreePointer(string(content), dir, root); err != nil {
					return "", true, err
				}
				return root, true, nil
			}
			if _, ok := ParseGitdirPointer(string(content)); !ok {
				return "", true, errors.New("malformed Git worktree pointer")
			}
			if err := validateSubmodulePointer(string(content), dir); err != nil {
				return "", true, err
			}
			return gitBinaryMainRootLocal(cwd)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

func pointerTarget(content, ancestor string) (string, bool) {
	gitdir, ok := ParseGitdirPointer(content)
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(ancestor, gitdir)
	}
	return filepath.Clean(gitdir), true
}

func validateLinkedWorktreePointer(content, ancestor, root string) error {
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
	common := filepath.Dir(filepath.Dir(target))
	mainGit := filepath.Join(root, ".git")
	mainInfo, err := os.Stat(mainGit)
	if err != nil {
		return fmt.Errorf("main checkout Git directory %q: %w", mainGit, err)
	}
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

func gitBinaryMainRootLocal(cwd string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	common, ok := runGitCmd(ctx, cwd, "rev-parse", "--git-common-dir")
	if !ok || common == "" {
		return "", true, errors.New("git common directory could not be resolved")
	}
	candidate := MainRootCandidateFromCommonDir(cwd, common)
	if candidate != "" && GitEntryResolvesToCommon(candidate, common) {
		return candidate, true, nil
	}
	top, ok := runGitCmd(ctx, cwd, "rev-parse", "--show-toplevel")
	if !ok || top == "" {
		return "", true, errors.New("git checkout root could not be resolved")
	}
	return resolveClean(top), true, nil
}

func runGitCmd(ctx context.Context, dir string, args ...string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = filteredGitEnvironment(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

var repositorySelectionEnvironment = map[string]struct{}{
	"GIT_DIR": {}, "GIT_WORK_TREE": {}, "GIT_COMMON_DIR": {}, "GIT_INDEX_FILE": {},
	"GIT_OBJECT_DIRECTORY": {}, "GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
	"GIT_CEILING_DIRECTORIES": {}, "GIT_DISCOVERY_ACROSS_FILESYSTEM": {},
}

func filteredGitEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		blocked := false
		for repositoryKey := range repositorySelectionEnvironment {
			if strings.EqualFold(key, repositoryKey) {
				blocked = true
				break
			}
		}
		if !blocked {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
