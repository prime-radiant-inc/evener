//go:build unix

package identifier

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openRegularNoFollow opens path for reading through a single descriptor that
// is validated before any read: the open refuses a symlink at the final
// component (ELOOP, the target is never touched) and never blocks — a
// read-only open of a FIFO would otherwise block until a writer appears, so
// the open carries O_NONBLOCK, harmless for regular files. The descriptor is
// then fstat'd and must be a regular file; anything else (a FIFO, a device) is
// refused. The validation and the read share one descriptor, so nothing can
// be swapped between the check and the bytes.
func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("open %q: leaf path is a symlink, refusing to follow it", path)
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("open %q: no file for descriptor", path)
	}
	info, serr := file.Stat()
	if serr != nil {
		_ = file.Close()
		return nil, serr
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("open %q: %w", path, ErrNotRegularFile)
	}
	return file, nil
}
