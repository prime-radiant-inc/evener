//go:build unix

package hub

import (
	"os"
	"syscall"
)

// fileOwnerUID reports the uid that owns info, which unix systems record.
func fileOwnerUID(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// fileModePerm reports the POSIX permission bits info carries, judged: unix
// records them, so the caller can hold a file to the 0600 this hub writes.
func fileModePerm(info os.FileInfo) (os.FileMode, bool) {
	return info.Mode().Perm(), true
}
