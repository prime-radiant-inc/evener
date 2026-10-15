//go:build darwin

package sandbox

import (
	"path/filepath"
	"strings"
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

// diskSpelling returns the existing directory p as the filesystem stores it,
// walking p one component at a time from / without following any symlink: a
// path that runs through a link is not the directory it names, so a link
// planted at a reported path cannot pass for the directory it points to.
func diskSpelling(p string) (string, bool) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", false
	}
	for comp := range strings.SplitSeq(strings.TrimPrefix(filepath.Clean(p), "/"), "/") {
		if comp == "" {
			continue
		}
		next, err := unix.Openat(fd, comp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return "", false
		}
		fd = next
	}
	defer func() { _ = unix.Close(fd) }()
	s, err := FdPath(fd)
	return s, err == nil && s != ""
}
