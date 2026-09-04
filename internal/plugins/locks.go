package plugins

import (
	"context"
	"fmt"
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
// Every mutation of the store passes through here, and creating the lock file
// is the mutation's first write, so this is the one place that turns away a
// root which resolves against whatever directory the process happens to be in
// — an empty root (none could be resolved) or a relative one. Calling
// storeRootError used to be each caller's own job: the nine writers that take
// the lock and then write (install, upgrade, remove, the two flag setters, gc,
// and the three marketplace mutations) skipped it and planted a store in
// somebody's project, and Browse, which clones a marketplace lazily, skipped
// it too. Seeding checked for itself and still does. Here a writer cannot
// forget the check without also forgetting the lock.
//
// Locking is not the whole of it. A caller that reads or creates something
// before it locks — resolveForLaunch, which never locks at all; the update
// sweeps, which enumerate the registry first and on a broken root wrote
// nothing but reported a clean sweep of a store that was not there; List and
// ListMarketplaces, which only read — is refused by storePath instead, where
// the path it would have used is derived.
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
