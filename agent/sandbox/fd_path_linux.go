//go:build linux

package sandbox

import (
	"os"
	"strconv"
)

// FdPath returns the kernel's path for an open fd via /proc/self/fd.
func FdPath(fd int) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
}
