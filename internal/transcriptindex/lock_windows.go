//go:build windows

package transcriptindex

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes a whole-file lock, waiting for it: shared for readers,
// exclusive for the one extender. The hub and a daemon open the same sidecar,
// so the lock is a file lock rather than a mutex.
func lockFile(f *os.File, exclusive bool) error {
	var flags uint32
	if exclusive {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
}

// lockFileTry takes the whole-file lock without waiting. Contention returns
// the error isLockContention names; every other failure is a lock fault the
// refusing handle must surface as itself, not fold into ErrLockBusy.
func lockFileTry(f *os.File, exclusive bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
}

// isLockContention reports whether err is the error LockFileEx answers with
// LOCKFILE_FAIL_IMMEDIATELY when another writer holds the sidecar — the one
// failure that means contention. Every other error is a fault on the lock
// itself.
func isLockContention(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
