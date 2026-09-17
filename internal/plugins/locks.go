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

// StoreChanges reports which of the two store files a call actually
// persisted to, independent of whether the call itself succeeded: a write
// that lands and then hits a failing later step, a lazy fetch's backfill, or
// lockStore's own legacy-name migration all leave one or both true. It is the
// one signal every manager method that can silently change either file
// returns beside its error (never folded into the error, which is how the
// same fact used to be encoded three different ways before it), so a caller
// answers "do I owe a broadcast" the same way regardless of which of those
// caused it, or whether the call also failed for an unrelated reason.
//
// A caller that owns its own applied-write signal (writeDidApply in the hub)
// broadcasts on changes.X || writeDidApply(err) for a write endpoint; a plain
// read endpoint (list, browse) broadcasts on changes.X alone - writeDidApply
// answers "did a write that this call was itself responsible for land", not
// "did nothing fail", so it must not be OR'd in for a call that made no
// write of its own (writeDidApply(nil) is true, which would broadcast on
// every successful read otherwise).
type StoreChanges struct {
	Marketplaces bool
	Plugins      bool
}

// merge combines two StoreChanges, true wherever either says a store
// changed - the shape a caller that layers one call's changes onto another's
// uses (lockStore's migration alongside catalogPlugin's own lazy fetch).
func (c StoreChanges) merge(other StoreChanges) StoreChanges {
	return StoreChanges{
		Marketplaces: c.Marketplaces || other.Marketplaces,
		Plugins:      c.Plugins || other.Plugins,
	}
}

// lockStore takes the store lock and, holding it, renames every marketplace
// recorded under a name the store refuses today (migrateMarketplaceNames).
// Every mutation and every lazy fetch locks here, so under the store lock
// every recorded marketplace name is valid: an operation can derive an
// entry's directories from its name and split a registry key at its last
// '@' without asking which evener wrote the name. A migration that fails
// releases the lock and fails the acquisition, so no operation runs on a
// half-migrated store. Establishing the invariant means reading the
// marketplaces file on every acquisition, so a file that cannot be parsed
// fails even the mutations that never read it themselves — the flag setters,
// a plugin removal, the gc sweep — and the error names the file the user has
// to fix, which evener-doctor reports too. The bundled lock and Doctor's
// read-only wait on this lock are the two acquisitions that do not come
// through here.
//
// changes is migrateMarketplaceNames's own answer, returned even when the
// migration fails: it renames each refused name independently (each one
// saved on its own), so a failure partway through never undoes an earlier
// iteration's already-persisted rename - the whole acquisition owes the
// broadcast for what did land, not only for a fully clean run. A caller
// whose own request is a plain read (ListMarketplaces, List, Browse) gets
// this the same way a write does.
func (m *Manager) lockStore(ctx context.Context, acquire lockAcquirer, timeout time.Duration) (release func(), changes StoreChanges, err error) {
	release, err = m.acquireStoreLock(ctx, acquire, m.lockPath(), timeout)
	if err != nil {
		return nil, StoreChanges{}, err
	}
	changes, err = m.migrateMarketplaceNames()
	if err != nil {
		release()
		return nil, changes, err
	}
	return release, changes, nil
}

// migrateStore takes the store lock for nothing but the migration lockStore
// runs on the way in, and releases it again. The sweeps (UpdateAll,
// UpdateAutoUpgrade) call this before they enumerate the registry, because
// the keys they enumerate have to postdate the migration: otherwise the first
// per-plugin upgrade's own lock performs it and the rest of the sweep asks
// for keys the rename has just replaced. The lock is released rather than
// held across those upgrades, each of which takes it itself.
func (m *Manager) migrateStore(ctx context.Context) error {
	release, _, err := m.lockStore(ctx, installAcquireLock, 30*time.Second)
	if err != nil {
		return err
	}
	release()
	return nil
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

// acquireExistingLock waits on a lock file that is already there, opening it
// read-only, and hands back a release that does nothing when there is none.
// Both differences from acquireLock make the same point: waiting on a lock is
// not a write.
//
// Doctor's orphaned-cache walk reads under the lock, so acquireLock's O_CREATE
// would leave a lock file behind in a store that has none — the one mutation a
// read-only verb would make in a store that already exists. The read-only open
// is the other half: flock is granted on a descriptor opened either way, so
// asking for write access only turned a store the caller may read but not
// write (root-owned, or a read-only mount) into a lock complaint where an
// unlocked walk had reported its orphans fine.
//
// Walking unlocked when there is no lock file is safe because the writers
// create theirs before they touch anything else: no lock file means no writer
// has ever locked this store. In a real store the case cannot arise at all —
// the cache directory the walk is there to read was made by a writer, which
// created the lock file first. The window that is left needs the lock file
// deleted out of band and a writer recreating it in the moment after this open
// failed, which leaves the walk exactly where every walk was before it took a
// lock at all.
func acquireExistingLock(ctx context.Context, lockPath string, timeout time.Duration) (func(), error) {
	f, err := lockOpenFile(lockPath, os.O_RDONLY, 0)
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
