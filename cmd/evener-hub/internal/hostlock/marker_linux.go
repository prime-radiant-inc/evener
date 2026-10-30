//go:build linux

package hostlock

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockMarker(file *os.File) error {
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	return unix.FcntlFlock(file.Fd(), unix.F_OFD_SETLK, &lock)
}
