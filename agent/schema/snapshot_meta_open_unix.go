//go:build unix

package schema

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// readFileNoFollowOS opens path through a single descriptor that refuses a
// symlink at the final component (O_NOFOLLOW) and reads all bytes from it.
//
// Unlike execenv.OpenRegularNoFollow, this does NOT carry O_NONBLOCK and does
// NOT fstat for regular: a FIFO at the .meta.json path is a deliberate
// synchronization barrier in retirement tests (retirementPauseColdClaim) and
// must be allowed to block the open. O_NOFOLLOW is harmless for FIFOs and
// regular files — it only refuses when the final path component is itself a
// symlink (returning ELOOP, never touching the target). The validation and
// the read share one descriptor, so nothing can be swapped between the check
// and the bytes.
func readFileNoFollowOS(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
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
	defer file.Close()
	return io.ReadAll(file)
}
