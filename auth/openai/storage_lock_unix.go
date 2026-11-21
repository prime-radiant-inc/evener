//go:build linux || darwin

package openai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// authStoreLockPollInterval paces acquisition: a non-blocking try plus a short
// wait keeps the wait cancellable, where a blocking flock would ignore ctx.
const authStoreLockPollInterval = 20 * time.Millisecond

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
func acquireAuthStoreLock(ctx context.Context, stateDir string) (func(), error) {
	dir := filepath.Join(stateDir, authDirName)
	f, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open auth store for locking: %w", err)
	}
	notify := installedAuthStoreLockWaiting()
	notified := false
	for {
		flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		// Only contention retries: a bad descriptor or any other error fails
		// fast rather than polling to the caller's deadline. EWOULDBLOCK is
		// EAGAIN on both GOOS this file builds for.
		if !errors.Is(flockErr, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("flock auth store: %w", flockErr)
		}
		if notify != nil && !notified {
			notified = true
			notify()
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(authStoreLockPollInterval):
		}
	}
}
