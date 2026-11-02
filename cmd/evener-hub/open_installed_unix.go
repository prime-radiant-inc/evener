//go:build unix

package hub

import (
	"os"

	"golang.org/x/sys/unix"
)

// openInstalledRead opens path without blocking on a FIFO. The restart
// verifies the pinned binary while holding hubUpdateMu, and a target swapped
// for a FIFO between scheduling and verification must fail rather than park
// the hub. The caller validates the descriptor before reading.
func openInstalledRead(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
