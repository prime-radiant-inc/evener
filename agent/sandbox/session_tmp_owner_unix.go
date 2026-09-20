//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package sandbox

import (
	"os"
	"syscall"
)

// scratchEntryOwnedByProcess reports whether path is owned by this process's
// user. A session temp container is created in a world-usable host temp, so a
// directory that is not ours must never be adopted as ours: the caller fails
// closed on false.
func scratchEntryOwnedByProcess(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, nil
	}
	return int(stat.Uid) == os.Getuid(), nil
}
