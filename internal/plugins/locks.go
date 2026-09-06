package plugins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type lockFile interface {
	Fd() uintptr
	Close() error
}

var (
	lockMkdirAll = os.MkdirAll
	lockOpenFile = func(name string, flag int, perm os.FileMode) (lockFile, error) {
		return os.OpenFile(name, flag, perm)
	}
	lockNow   = time.Now
	lockSleep = time.Sleep
)

// lockAcquirer is acquireLock's signature, which the per-area test seams
// (installAcquireLock and its siblings) stand in for.
type lockAcquirer func(context.Context, string, time.Duration) (func(), error)

// acquireStoreLock refuses a store root that cannot be used, then takes the
// store lock at lockPath through acquire.
//
// storePath refuses the same roots wherever a store path is derived, which
// covers everything a writer goes on to touch. It cannot cover the lock file:
// the lock is taken before any of those paths is derived, and its own path is
// the plain join m.lockPath(). So this is the second guard, on the first write
// every mutation makes. Calling storeRootError used to be each caller's own
// job: the nine writers that take the lock and then write (install, upgrade,
// remove, the two flag setters, gc, and the three marketplace mutations)
// skipped it and planted a store in somebody's project, and Browse, which
// clones a marketplace lazily, skipped it too. Here a writer cannot forget the
// check without also forgetting the lock.
//
// Three checks are written out on top of storePath, and these are all of them.
// This one, because the lock file is created before any store path is derived.
// resolveForLaunch, because a launch continues without plugins rather than
// failing, so it needs the unusable store as a diagnostic and not an error.
// Doctor, because reporting the environment is what Doctor is for, so it needs
// a FAIL finding and not an error. Everywhere else derives through storePath.
func (m *Manager) acquireStoreLock(ctx context.Context, acquire lockAcquirer, lockPath string, timeout time.Duration) (func(), error) {
	if err := m.storeRootError(); err != nil {
		return nil, err
	}
	return acquire(ctx, lockPath, timeout)
}

// acquireBundledLock takes the bundled cache's lock for one mutation of
// <Root>/bundled. Every bundled-cache mutation acquires here and nowhere else,
// and it goes through acquireStoreLock so the store-root check lands on this
// lock the same way it lands on the store lock.
func (m *Manager) acquireBundledLock(ctx context.Context, timeout time.Duration) (func(), error) {
	return m.acquireStoreLock(ctx, acquireLock, m.bundledLockPath(), timeout)
}

// acquireLock takes an exclusive flock on lockPath, retrying with capped
// exponential backoff until ctx is canceled or timeout elapses. The returned
// release unlocks and closes the file. Callers without a request context pass
// context.Background(). Cancellation is observed within one backoff interval
// (≤200ms), so a disconnected client's handler stops waiting promptly instead
// of spinning out the full timeout.
func acquireLock(ctx context.Context, lockPath string, timeout time.Duration) (func(), error) {
	if err := lockMkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating lock parent: %w", err)
	}
	f, err := lockOpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	return flockUntil(ctx, f, lockPath, timeout)
}

// acquireExistingLock is acquireLock without the O_CREATE: it waits on a lock
// file that is already there and, when there is none, hands back a release
// that does nothing.
//
// Doctor's orphaned-cache walk reads under the lock rather than writing, and
// creating the lock file is a write — the one mutation a read-only verb would
// leave behind in a store that already exists. It can walk unlocked when there
// is no lock file, because the writers create theirs before they touch
// anything else: no lock file means no writer has ever locked this store, so
// nothing can be materializing or collecting under the walk. (In a real store
// the case cannot arise at all: the cache directory the walk is there to read
// was created by a writer, which created the lock file first.)
func acquireExistingLock(ctx context.Context, lockPath string, timeout time.Duration) (func(), error) {
	f, err := lockOpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return func() {}, nil
		}
		return nil, fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	return flockUntil(ctx, f, lockPath, timeout)
}

// flockUntil is the wait both acquires share: retry the exclusive flock on an
// already-open lock file until it is granted, ctx is canceled, or timeout
// elapses.
func flockUntil(ctx context.Context, f lockFile, lockPath string, timeout time.Duration) (func(), error) {
	deadline := lockNow().Add(timeout)
	backoff := 10 * time.Millisecond
	for {
		if cerr := ctx.Err(); cerr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("waiting for plugin lock %s: %w", lockPath, cerr)
		}
		err := lockFlock(int(f.Fd()), lockOpExclusiveNB)
		if err == nil {
			return func() {
				_ = lockFlock(int(f.Fd()), lockOpUnlock)
				_ = f.Close()
			}, nil
		}
		if !isLockContended(err) {
			_ = f.Close()
			return nil, fmt.Errorf("flock %s: %w", lockPath, err)
		}
		if lockNow().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("another evener plugin operation is in progress (locked: %s)", lockPath)
		}
		lockSleep(backoff)
		backoff *= 2
		if backoff > 200*time.Millisecond {
			backoff = 200 * time.Millisecond
		}
	}
}
