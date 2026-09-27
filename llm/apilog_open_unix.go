//go:build darwin || linux

package llm

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openPrivateAPILogFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open API-log target %q", path)
	}
	locked := false
	closeOnError := func(err error) (*os.File, error) {
		if locked {
			_ = closePrivateAPILogFile(file)
		} else {
			_ = file.Close()
		}
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		return closeOnError(err)
	}
	if !info.Mode().IsRegular() {
		return closeOnError(fmt.Errorf("API-log target %q is not a regular file", path))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return closeOnError(fmt.Errorf("%w: %s", ErrAPILogTargetLocked, path))
		}
		return closeOnError(fmt.Errorf("lock API-log target %q: %w", path, err))
	}
	locked = true
	if err := file.Chmod(0o600); err != nil {
		return closeOnError(err)
	}
	if err := recoverCanonicalAPILogTail(file, canonicalAPILogMaxLineBytes); err != nil {
		return closeOnError(err)
	}
	return file, nil
}

func closePrivateAPILogFile(file *os.File) error {
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock API log: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close API log: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
