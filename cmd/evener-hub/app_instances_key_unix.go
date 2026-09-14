//go:build unix

package hub

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openEndpointFingerprintKey opens the key file at path for reading without
// following a symlink: O_NOFOLLOW makes a link at the path fail the open, so the
// refusal is atomic with getting the descriptor. The caller judges and reads
// that same descriptor, which is what closes the window a check-then-read-by-
// path left between the two. O_NONBLOCK keeps a FIFO or device at the path from
// blocking the open; the descriptor checks refuse whatever is not the hub's own
// regular file.
func openEndpointFingerprintKey(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open endpoint fingerprint key %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open endpoint fingerprint key %s", path)
	}
	return file, nil
}
