//go:build aix

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
	flock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &flock); err != nil {
		contended := errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN)
		_, _, closeErr := closeOnError(fmt.Errorf("lock installation ID: %w", err))
		return nil, contended && closeErr != nil, closeErr
	}
	return &installationIDFileLock{file: file}, false, nil
}

func (lock *installationIDFileLock) Release() error {
	flock := unix.Flock_t{Type: unix.F_UNLCK, Whence: 0, Start: 0, Len: 0}
	unlockErr := unix.FcntlFlock(lock.file.Fd(), unix.F_SETLK, &flock)
	closeErr := lock.file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock installation ID: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close installation ID lock: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
