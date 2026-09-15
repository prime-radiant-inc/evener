//go:build !windows

package skill

import "os"

// defaultSkillsBaseRoot is where the private per-user cache base lives. On the
// Unix platforms this is the temp dir, which a session confined to its worktree
// can still read.
func defaultSkillsBaseRoot() string { return os.TempDir() }
