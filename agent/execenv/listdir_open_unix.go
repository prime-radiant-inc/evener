//go:build unix

package execenv

import (
	"os"

	"golang.org/x/sys/unix"
)

// listDirOpenDir opens absPath for listing with the O_DIRECTORY fence: a
// readerless FIFO at the requested path would otherwise block the open
// itself — before any context check the reader behind it could answer —
// until a writer appears, so the walk must fail such targets ENOTDIR at the
// open instead of waiting on one. The flag constrains the target's type
// after a link resolves, never the link, so a symlink to a directory keeps
// traversing exactly as the legacy walk's followed open did.
func listDirOpenDir(absPath string) (*os.File, error) {
	return os.OpenFile(absPath, os.O_RDONLY|unix.O_DIRECTORY, 0)
}
