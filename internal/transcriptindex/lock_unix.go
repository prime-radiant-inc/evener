//go:build unix

package transcriptindex

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an advisory whole-file lock, waiting for it: shared for
// readers, exclusive for the one extender. The hub and a daemon open the same
// sidecar, so the lock is a file lock rather than a mutex.
func lockFile(f *os.File, exclusive bool) error {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	for {
		if err := unix.Flock(int(f.Fd()), how); !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// lockFileTry takes the advisory lock without waiting: shared for readers,
// exclusive for the one extender. Contention returns the errno
// isLockContention names; every other failure is a lock fault the refusing
// handle must surface as itself, not fold into ErrLockBusy.
func lockFileTry(f *os.File, exclusive bool) error {
	how := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		how = unix.LOCK_EX | unix.LOCK_NB
	}
	for {
		if err := unix.Flock(int(f.Fd()), how); !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// isLockContention reports whether err is the errno flock answers for a held
// lock under LOCK_NB — the one failure that means another writer holds the
// sidecar. Every other errno is a fault on the lock itself.
func isLockContention(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK)
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
