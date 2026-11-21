//go:build linux || darwin

package openai

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireAuthStoreLock takes an exclusive advisory flock on the auth directory
// under stateDir and returns the release. The directory itself is the lock
// target, so no lock file is left beside the records, and a read-only
// credential directory (a save that must fail on its own write) can still be
// opened for locking and still fails where it should.
//
// The lock is held across file writes only. Nothing in this package holds it
// across a token refresh's network call: a refresh re-checks the record under
// the lock before writing, which is what keeps a renamed or deleted record
// from being resurrected without making a slow issuer block other instances.
func acquireAuthStoreLock(stateDir string) (func(), error) {
	dir := filepath.Join(stateDir, authDirName)
	f, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open auth store for locking: %w", err)
	}
	// A test uses the contention seam to learn that an acquisition has parked
	// instead of guessing with a window. One non-blocking probe reports the
	// contention before the blocking call below waits; production leaves the
	// seam nil and pays no probe.
	if notify := installedAuthStoreLockWaiting(); notify != nil {
		switch probeErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
		case probeErr == nil:
			return releaseAuthStoreLock(f), nil
		case errors.Is(probeErr, syscall.EWOULDBLOCK):
			notify()
		}
	}
	for {
		flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if flockErr == nil {
			return releaseAuthStoreLock(f), nil
		}
		// A blocking flock is interrupted by a signal; retry it. Any other
		// error is the answer, not something to wait out.
		if !errors.Is(flockErr, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("flock auth store: %w", flockErr)
		}
	}
}

func releaseAuthStoreLock(f *os.File) func() {
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}
