//go:build serffuzz

package sandbox

import (
	"os"
	"path/filepath"
)

func requireGitHarness(TestingT) {}

func gitHarness(t TestingT, dir string, args ...string) {
	t.Helper()
	switch args[0] {
	case "init":
		gitDir := filepath.Join(dir, ".git")
		for _, path := range []string{filepath.Join(gitDir, "objects"), filepath.Join(gitDir, "refs"), filepath.Join(gitDir, "logs"), filepath.Join(gitDir, "hooks"), filepath.Join(gitDir, "worktrees")} {
			_ = os.MkdirAll(path, 0o755)
		}
		_ = os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\nrepositoryformatversion = 0\n"), 0o644)
	case "commit":
		return
	case "worktree":
		path := args[len(args)-1]
		gitDir := filepath.Join(dir, ".git", "worktrees", filepath.Base(path))
		_ = os.MkdirAll(gitDir, 0o755)
		_ = os.MkdirAll(path, 0o755)
		_ = os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644)
	}
}
