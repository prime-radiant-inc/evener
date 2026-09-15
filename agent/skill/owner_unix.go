//go:build unix

package skill

import (
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// processOwnerTag names the current user for the private cache directory, so
// two users on a shared host never contend for the same path.
func processOwnerTag() string { return strconv.Itoa(os.Getuid()) }

// cacheDirOwnedByCurrentUser reports whether info describes a directory owned
// by this process's user. A directory this process did not create must not be
// trusted with skill content the agent will read.
func cacheDirOwnedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// cacheDirHasPrivatePermissions reports whether a cache directory withholds all
// group and other access.
func cacheDirHasPrivatePermissions(info fs.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}

// cacheDirOwnerCanWrite reports whether the owner can create entries inside the
// directory, which a cache root must allow.
func cacheDirOwnerCanWrite(info fs.FileInfo) bool {
	return info.Mode().Perm()&0o700 == 0o700
}

// tempRootTrusted reports whether a root may hold a per-user cache: one carrying
// the sticky bit, or one this process owns with no group or other write, so no
// other user can replace the per-user entry between validation and use.
func tempRootTrusted(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSticky != 0 {
		// A sticky root only protects this process's entries from users who do not
		// own the root: its owner can still replace them, so the root must be
		// owned by this process or by root for the sticky bit to mean anything.
		return cacheDirOwnerIsUsOrRoot(info)
	}
	return cacheDirOwnedByCurrentUser(info) && info.Mode().Perm()&0o022 == 0
}

// cacheDirOwnerIsUsOrRoot reports whether info is owned by this process's user
// or by root, the only owners whose sticky directory protects this process's
// entries from everyone else.
func cacheDirOwnerIsUsOrRoot(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (int(stat.Uid) == os.Getuid() || stat.Uid == 0)
}

// processCopyRootTrusted reports whether a root may hold the process-lifetime
// extraction. On Unix it is tempRootTrusted: a temp root is verified the same
// way whether it holds the shared cache or the one private copy, so the degraded
// path accepts exactly the roots the shared cache would.
func processCopyRootTrusted(info fs.FileInfo) bool { return tempRootTrusted(info) }

// ancestorDirTrusted reports whether a directory above the cache root cannot be
// replaced by other users: it carries the sticky bit, or it is owned by this
// user or by root with no group or other write. Ownership by root is accepted
// here, unlike for the root itself, because the platform's temp chain above a
// user's own directory is normally root-owned; what matters is that no other
// user can rename the directories on the way to the cache.
func ancestorDirTrusted(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSticky != 0 {
		return cacheDirOwnerIsUsOrRoot(info)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Getuid() || stat.Uid == 0
}
