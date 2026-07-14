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
	for _, line := range strings.Split(content, "\n") {
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
	common = resolveClean(common)
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
				return root, true, nil
			}
			if _, ok := ParseGitdirPointer(string(content)); !ok {
				return "", true, errors.New("malformed Git worktree pointer")
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

func gitBinaryMainRootLocal(cwd string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	common, ok := runGitCmd(ctx, cwd, "rev-parse", "--git-common-dir")
	if !ok || common == "" {
		return "", true, errors.New("Git common directory could not be resolved")
	}
	candidate := MainRootCandidateFromCommonDir(cwd, common)
	if candidate != "" && GitEntryResolvesToCommon(candidate, common) {
		return candidate, true, nil
	}
	top, ok := runGitCmd(ctx, cwd, "rev-parse", "--show-toplevel")
	if !ok || top == "" {
		return "", true, errors.New("Git checkout root could not be resolved")
	}
	return resolveClean(top), true, nil
}

func runGitCmd(ctx context.Context, dir string, args ...string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}
