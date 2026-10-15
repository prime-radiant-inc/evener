//go:build linux || darwin

package hostlock

import (
	"errors"
	"os"
	"syscall"
)

func lockOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
func flockInspectionSupported() bool { return true }

func flockIsHeld(f *os.File) (bool, error) {
	err := flockExclusiveNonBlocking(f)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, flockUnlock(f)
}

func flockExclusiveNonBlocking(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func flockUnlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
