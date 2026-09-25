//go:build windows

package transcriptindex

import (
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

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
