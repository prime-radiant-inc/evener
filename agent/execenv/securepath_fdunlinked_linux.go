//go:build linux

package execenv

import "golang.org/x/sys/unix"

// fdUnlinked reports whether fd references a directory whose link count is zero:
// a directory deleted from its parent. A directory removed by rmdir — or, with
// its contents, by a recursive delete — drops to st_nlink 0 while a held
// descriptor keeps its inode reachable; a directory merely renamed away, or a
// root whose logical path never existed, keeps a positive count. It is a
// descriptor check, never a path one, so it is unaffected by a concurrent swap
// and it distinguishes "unlinked" from "path missing" (issue #4680).
func fdUnlinked(fd int) bool {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false
	}
	return st.Nlink == 0
}
