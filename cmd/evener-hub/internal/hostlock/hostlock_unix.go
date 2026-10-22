//go:build linux || darwin

package hostlock

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
func flockInspectionSupported() bool { return true }

func flockIsHeld(f *os.File) (bool, error) {
	// Ordinary GETLK sees even this process's OFD ownership on Linux, unlike
	// OFD_GETLK on the owning description. Darwin exposes flock to GETLK.
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	err := unix.FcntlFlock(f.Fd(), unix.F_GETLK, &lock)
	return lock.Type != unix.F_UNLCK, err
}

func flockExclusiveNonBlocking(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func openStorageDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
