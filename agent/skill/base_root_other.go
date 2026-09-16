//go:build !windows

package skill

import "os"

// defaultSkillsBaseRoot is where the private per-user cache base lives. On the
// Unix platforms this is the temp dir, which a session confined to its worktree
// can still read; the base's ownership and permission checks verify the per-user
// name inside it.
func defaultSkillsBaseRoot() (string, error) { return os.TempDir(), nil }
