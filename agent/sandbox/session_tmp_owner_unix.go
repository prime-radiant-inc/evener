//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package sandbox

import (
	"os"
	"syscall"
)

// SessionTmpSupported reports whether this platform can host a world-usable
// session temp container (session_tmp.go). The container's whole contract is POSIX
// mode bits — a 0711 container a foreign uid can traverse and a 1777 sticky leaf
// it can write — and its reason to exist is a descendant that deliberately becomes
// another user. Neither holds on Windows, where callers keep the pre-existing
// session-scratch export instead of switching TMPDIR to a container that cannot
// exist.
const SessionTmpSupported = true

// scratchEntryOwnedByProcess reports whether path is owned by this process's
// user. A session temp container is created in a world-usable host temp, so a
// directory that is not ours must never be adopted as ours: the caller fails
// closed on false.
func scratchEntryOwnedByProcess(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return scratchInfoOwnedByProcess(info), nil
}

// scratchInfoOwnedByProcess is scratchEntryOwnedByProcess over a stat the
// caller already holds: per-request serving paths validate each entry with
// one Lstat and read the uid from its result, never re-statting an entry the
// same call just described — a second stat would re-resolve the same inode
// for a value the first already carried, and the entry could change hands
// between the two.
func scratchInfoOwnedByProcess(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Getuid()
}
