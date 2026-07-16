//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package sandbox

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type unixScratchLease struct {
	file *os.File
}

func acquireScratchLease(path string) (scratchLease, bool, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open lease: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	closeWith := func(cause error) error {
		if closeErr := file.Close(); closeErr != nil {
			return errors.Join(cause, fmt.Errorf("close lease: %w", closeErr))
		}
		return cause
	}
	info, err := file.Stat()
	if err != nil {
		return nil, false, closeWith(fmt.Errorf("stat lease: %w", err))
	}
	if !info.Mode().IsRegular() {
		return nil, false, closeWith(errors.New("lease is not a regular file"))
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, false, closeWith(fmt.Errorf("secure lease: %w", err))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		contended := errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
		return nil, contended, closeWith(fmt.Errorf("lock lease: %w", err))
	}
	return &unixScratchLease{file: file}, false, nil
}

func (lease *unixScratchLease) Release() error {
	unlockErr := unix.Flock(int(lease.file.Fd()), unix.LOCK_UN)
	closeErr := lease.file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock lease: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close lease: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
