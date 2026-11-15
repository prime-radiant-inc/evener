//go:build !windows

package skill

import "os"

// defaultSkillsBaseRoot is where the private per-user cache base lives. On the
// Unix platforms this is the temp dir, which a session confined to its worktree
// can still read; the base's ownership and permission checks verify the per-user
// name inside it.
func defaultSkillsBaseRoot() (string, error) { return os.TempDir(), nil }

// defaultSkillsFallbackRoot is the verified root used when the configured temp
// dir cannot be trusted. It is the platform temp root, not TMPDIR, so it does
// not inherit TMPDIR's ancestor chain. Inside a sandbox /tmp is the fresh
// per-sandbox mount; on a host it is the shared platform temp dir, and the same
// ownership and per-user cache-dir checks still apply. The diagnostic names
// the substitution.
func defaultSkillsFallbackRoot() string { return "/tmp" }
