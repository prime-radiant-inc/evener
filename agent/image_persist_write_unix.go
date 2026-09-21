//go:build darwin || linux

package agent

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// createAttachmentFile creates the attachment at path for writing, refusing
// both to follow a symlink at the leaf and to overwrite an existing entry:
// the stored name is content-addressed, so an existing entry is either this
// same attachment from an earlier paste (the caller dedupes by comparing
// bytes) or a planted file that must never be silently replaced. os.ErrExist
// and os.ErrNotExist-style errors pass through to the caller as-is.
func createAttachmentFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("create attachment file %q", path)
	}
	return file, nil
}
