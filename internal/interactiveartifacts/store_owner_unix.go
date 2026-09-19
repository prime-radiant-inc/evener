//go:build unix

package interactiveartifacts

import (
	"os"
	"syscall"
)

func fileOwner(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}
