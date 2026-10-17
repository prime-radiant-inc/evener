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
//   - after a recursive delete the reported path stays the stale spelling and no
//     longer resolves, and
//   - after the path is recreated in place it resolves to a DIFFERENT file.
//
// A directory merely renamed away reports its NEW path and keeps its identity,
// so it is not dead; the confined-file root that legitimately carries a live
// descriptor whose captured spelling is absent reports its current real path
// and is not dead either. The path is the kernel's own, so this is not the
// path-based check the descriptor route exists to avoid: it is a property of
// the pinned inode, unaffected by a later spelling.
//
// Deleting is inferred from a NEGATIVE observation of the reported path, and a
// negative observation is only trustworthy if the path itself did not move
// underneath us. A concurrent rename between the F_GETPATH and the stat makes
// the previously reported path vanish or name a replacement, which would read
// as deletion for a descriptor that is still perfectly usable. So every
// negative result is re-validated: F_GETPATH is asked again, and if the
// descriptor's path has CHANGED, the observation belongs to a stale spelling,
// not to a dead directory — inconclusive, so the operation falls through to its
// normal resolution. Only a negative observation that survives a second
// F_GETPATH returning the SAME path is reported as deletion. A stat error other
// than absence (EACCES from an unsearchable ancestor) is inconclusive for the
// same reason.
// fdUnlinkedPath is sandbox.FdPath behind a variable so a test can force the
// concurrent-rename window deterministically: the check queries the descriptor's
// path, then stats it, and a rename between the two is exactly what must read as
// inconclusive. A seam here is the only way to place an event in that window
// without a racer.
var fdUnlinkedPath = sandbox.FdPath

func fdUnlinked(fd int) bool {
	path, err := fdUnlinkedPath(fd)
	if err != nil || path == "" {
		return false // cannot tell: leave the operation to its normal resolution
	}
	var st, cur unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false
	}
	absent, recreated := false, false
	if err := unix.Stat(path, &cur); err != nil {
		if !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENOTDIR) {
			return false // not absence: inconclusive
		}
		absent = true
	} else if st.Dev != cur.Dev || st.Ino != cur.Ino {
		recreated = true
	} else {
		return false // present, same file: alive
	}
	// A negative observation (the reported path is gone, or it now names a
	// different file). It only means the directory died if the path itself did
	// not move underneath us: a rename in the window makes the observation
	// belong to a stale spelling, and the descriptor is still perfectly usable.
	// Ask the kernel for the descriptor's current path again and treat any
	// change as inconclusive.
	now, err := fdUnlinkedPath(fd)
	if err != nil || now == "" || now != path {
		return false
	}
	return absent || recreated
}
