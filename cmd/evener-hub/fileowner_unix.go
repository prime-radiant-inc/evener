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
