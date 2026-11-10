//go:build unix

package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

// openCommondir opens a commondir for reading without following a symlink at
// the final component. The caller has already resolved the path and proven it
// inside the retained metadata roots; O_NOFOLLOW keeps a concurrent leaf swap
// from turning that proof into a read of a masked file.
func openCommondir(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
