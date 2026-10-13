//go:build unix

package execenv

import (
	"errors"
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

// admitOptions selects what an opened descriptor may be admitted as.
type admitOptions struct {
	// allowDir admits a directory: the fs.FS Open contract serves one.
	allowDir bool
	// admitDevice admits a device, which a search's own named target may be.
	admitDevice bool
}

// admitOpenedFD is the single admission constructor for a file opened
// O_RDONLY|O_CLOEXEC|O_NONBLOCK. It classifies the OPENED DESCRIPTOR, never
// the caller's spelling about it: fstat gives the type, fdOnPseudoMount gives
// whether the descriptor lives on a pseudo filesystem, so no alias, bind
// mount, or classify-then-open swap can hide a stream that stats regular
// (/proc/kmsg) behind an ordinary spelling (#4574 round 11). streamed is the
// caller's spelled hint, kept only as the seam a stub-backed test root needs
// where the descriptor's real mount cannot be faked. A FIFO
// or socket is refused — a read-only open of one would otherwise block until a
// peer appears, and nothing bounds that wait, because the read cap applies
// only after a successful open (#4574 round 1) — and so is any other
// non-regular entry except a device when admitDevice is set. A device, or a
// regular file the classification routes as a stream, keeps O_NONBLOCK so the
// runtime poller owns the descriptor and a read deadline can interrupt it;
// every other admitted descriptor gets the ordinary blocking. On error the fd
// is closed and the caller owns nothing.
func admitOpenedFD(fd int, name string, opts admitOptions, streamed bool) (*os.File, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open %q: no file for descriptor", name)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		if !opts.allowDir {
			_ = file.Close()
			return nil, errNotRegularFile
		}
	case mode.IsRegular():
		if streamed || fdOnPseudoMount(fd) {
			return file, nil
		}
	case opts.admitDevice && mode&os.ModeDevice != 0:
		return file, nil
	default:
		_ = file.Close()
		return nil, errNotRegularFile
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

// openRegularFollow opens path for reading through a single nonblocking
// descriptor admitted by admitOpenedFD, following a symlink at the final
// component the way the off arm's reads always have. It is the off arm's
// entry to the shared admission core, so the descriptor's own type and mount
// decide the read discipline on this arm exactly as on the sandboxed one.
func openRegularFollow(path string, admitDevice, streamed bool) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file, err := admitOpenedFD(fd, path, admitOptions{allowDir: true, admitDevice: admitDevice}, streamed)
	if err != nil {
		if errors.Is(err, errNotRegularFile) {
			return nil, fmt.Errorf("open %q: not a regular file", path)
		}
		return nil, err
	}
	return file, nil
}
