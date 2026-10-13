//go:build unix

package execenv

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenRegularNoFollow opens path for reading through a single descriptor that
// is validated before any read: the open refuses a symlink at the final
// component (ELOOP, the target is never touched) and never blocks — a
// read-only open of a FIFO would otherwise block until a writer appears, so
// the open carries O_NONBLOCK, harmless for regular files. The descriptor is
// then fstat'd and must be a regular file; anything else (a FIFO, a device) is
// refused. The validation and the read share one descriptor, so nothing can
// be swapped between the check and the bytes.
func OpenRegularNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if err == unix.ELOOP {
			return nil, fmt.Errorf("open %q: leaf path is a symlink, refusing to follow it", path)
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open %q: no file for descriptor", path)
	}
	info, serr := file.Stat()
	if serr != nil {
		_ = file.Close()
		return nil, serr
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("open %q: not a regular file", path)
	}
	return file, nil
}

// openRegularFollow opens path for reading through a single nonblocking
// descriptor whose type is confirmed before any read, following a symlink at
// the final component the way the off arm's reads always have. A FIFO or
// socket is refused — a read-only open of one would otherwise block until a
// peer appears, and nothing bounds that wait (#4574 round 1: a readerless
// FIFO under /tmp or /run hung a path=/ search indefinitely, because the
// read cap only applies after a successful open) — and so is any other
// non-regular entry except a device when admitDevice is set: a device named
// as a search target is still read, bounded by the caller's cap. A device
// keeps O_NONBLOCK — the runtime poller owns the descriptor, so a read
// deadline can interrupt a stream that never yields — while a regular file
// or directory gets the ordinary blocking descriptor back, except a
// regular file with streamed set: one whose path names pseudo-filesystem
// content (/proc/kmsg reports itself regular, and its read blocks in
// read(2) where no deadline can reach it) is a stream like a device, so it
// keeps O_NONBLOCK for the poller and the deadline (#4574 round 4).
func openRegularFollow(path string, admitDevice, streamed bool) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open %q: no file for descriptor", path)
	}
	info, serr := file.Stat()
	if serr != nil {
		_ = file.Close()
		return nil, serr
	}
	mode := info.Mode()
	admit := mode.IsRegular() || info.IsDir() || (admitDevice && mode&os.ModeDevice != 0)
	if !admit {
		_ = file.Close()
		return nil, fmt.Errorf("open %q: not a regular file", path)
	}
	if !mode.IsRegular() && !info.IsDir() {
		return file, nil
	}
	if mode.IsRegular() && streamed {
		return file, nil
	}
	flags, ferr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if ferr != nil {
		_ = file.Close()
		return nil, ferr
	}
	if _, ferr := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags&^unix.O_NONBLOCK); ferr != nil {
		_ = file.Close()
		return nil, ferr
	}
	return file, nil
}
