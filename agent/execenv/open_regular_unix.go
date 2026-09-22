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
