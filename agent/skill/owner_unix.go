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
		return true
	}
	return cacheDirOwnedByCurrentUser(info) && info.Mode().Perm()&0o022 == 0
}
