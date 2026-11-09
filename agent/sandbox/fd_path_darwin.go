//go:build darwin

package sandbox

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// FdPath returns the kernel's canonical path for an open fd via
// fcntl(F_GETPATH): the path as the filesystem stores it, real letter case and
// Unicode normalization included.
func FdPath(fd int) (string, error) {
	var buf [unix.PathMax]byte
	if err := fcntlGetPath(uintptr(fd), uintptr(unsafe.Pointer(&buf[0]))); err != nil {
		return "", err
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n]), nil
}

// fcntlGetPath runs fcntl(fd, F_GETPATH, buf); x/sys has no wrapper for it, so
// buf's address goes to FcntlInt as a plain integer. Converted anywhere else, a
// stack buffer's address goes stale when the goroutine's stack grows and moves
// during the call: the kernel writes the path into the old stack and the caller
// reads back an empty buffer (#3744). uintptrescapes makes the compiler move a
// buffer converted in this call's arguments to the heap, where it stays put.
//
//go:uintptrescapes
//go:noinline
func fcntlGetPath(fd, buf uintptr) error {
	_, err := unix.FcntlInt(fd, unix.F_GETPATH, int(buf))
	return err
}
