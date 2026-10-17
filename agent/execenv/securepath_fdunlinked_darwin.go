//go:build darwin

package execenv

import (
	"errors"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// fdUnlinked reports whether fd references a directory deleted from its parent.
//
// The Linux link-count test does not work on macOS: a directory that keeps a
// live descriptor does NOT drop st_nlink to zero, it stays at 2 (a recursive
// delete included), so st_nlink would never report a dead root and a stale-root
// mutation would slip through to its normal resolution (issue #4680). macOS has
// no O_PATH and no /proc, so ask the kernel for the descriptor's OWN path with
// fcntl(F_GETPATH) and check what that path names now:
//   - after a recursive delete the reported path no longer resolves, and
//   - after the path is recreated in place it resolves to a DIFFERENT file.
//
// A directory merely renamed away reports its new path and keeps its identity,
// so it is not dead; the confined-file root that legitimately carries a live
// descriptor whose captured spelling is absent reports its current real path
// and is not dead either. The path is the kernel's own, so this is not the
// path-based check the descriptor route exists to avoid: it is a property of
// the pinned inode, unaffected by a later spelling.
//
// Only ABSENCE means the directory is gone. Any other stat error — a renamed
// ancestor whose old spelling no longer resolves the way it did, or an ancestor
// the process can no longer search (EACCES) — leaves the outcome unknown, and an
// unknown outcome must NOT be reported as deletion: the descriptor may still be
// perfectly usable. Those cases are inconclusive, so the operation falls through
// to its normal resolution.
func fdUnlinked(fd int) bool {
	path, err := sandbox.FdPath(fd)
	if err != nil || path == "" {
		return false // cannot tell: leave the operation to its normal resolution
	}
	var st, cur unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false
	}
	if err := unix.Stat(path, &cur); err != nil {
		return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR)
	}
	return st.Dev != cur.Dev || st.Ino != cur.Ino // recreated in place: a different file
}
