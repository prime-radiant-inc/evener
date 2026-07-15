//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package installid

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type installationIDFileLock struct {
	file *os.File
}

func acquireInstallationIDFileLock(path string) (installationIDLock, bool, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open installation ID lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	closeOnError := func(err error) (installationIDLock, bool, error) {
		if closeErr := file.Close(); closeErr != nil {
			return nil, false, errors.Join(err, fmt.Errorf("close installation ID lock: %w", closeErr))
		}
		return nil, false, err
	}
	info, err := file.Stat()
	if err != nil {
		return closeOnError(fmt.Errorf("stat installation ID lock: %w", err))
	}
	if !info.Mode().IsRegular() {
		return closeOnError(fmt.Errorf("installation ID lock is not a regular file"))
	}
	if err := file.Chmod(0o600); err != nil {
		return closeOnError(fmt.Errorf("secure installation ID lock: %w", err))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		contended := errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
		_, _, closeErr := closeOnError(fmt.Errorf("lock installation ID: %w", err))
		return nil, contended && closeErr != nil, closeErr
	}
	return &installationIDFileLock{file: file}, false, nil
}

func (lock *installationIDFileLock) Release() error {
	unlockErr := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock installation ID: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close installation ID lock: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
