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
// exclusive for the one extender. Contention returns an error the refusing
// handle translates to ErrLockBusy.
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

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
