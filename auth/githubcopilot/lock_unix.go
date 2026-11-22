//go:build unix && !aix && !solaris

package githubcopilot

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockRecordFile takes a blocking exclusive flock on path, creating the lock
// file mode 0600 without following a planted symlink, and verifies the opened
// descriptor is a regular file. The returned function unlocks and closes it.
func lockRecordFile(path string) (func() error, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat lock file: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("lock file is not a regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("flock lock file: %w", err)
	}
	return func() error {
		unlockErr := syscall.Flock(fd, syscall.LOCK_UN)
		closeErr := file.Close()
		if unlockErr != nil {
			unlockErr = fmt.Errorf("unlock lock file: %w", unlockErr)
		}
		if closeErr != nil {
			closeErr = fmt.Errorf("close lock file: %w", closeErr)
		}
		return errors.Join(unlockErr, closeErr)
	}, nil
}
