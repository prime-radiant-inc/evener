//go:build unix && !aix && !solaris

package githubcopilot

import (
	"errors"
	"fmt"
	"syscall"
)

// lockRecordFile takes a blocking exclusive flock on path, creating the lock
// file mode 0600 without following a planted symlink, and verifies the opened
// descriptor is a regular file. The returned function closes the descriptor,
// releasing the lock.
func lockRecordFile(path string) (func() error, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("stat lock file: %w", err)
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fd)
		return nil, errors.New("lock file is not a regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("flock lock file: %w", err)
	}
	return func() error { return syscall.Close(fd) }, nil
}
