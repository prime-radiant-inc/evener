//go:build !unix

package worktree

// syncDir is a no-op where directory fsync is unsupported — for example
// Windows, where a directory handle cannot be flushed. The rename still commits
// atomically within the filesystem; only the extra durability barrier is
// unavailable.
func syncDir(string) error { return nil }
