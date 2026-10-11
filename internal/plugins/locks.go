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

// errLockContention marks the store lock still held when the wait times out.
// It carries the actionable, path-free reason - retry shortly - while the lock
// file's path stays in the surrounding error's text, so a scrubbing caller
// (EditMarketplace's editFailed) can keep the reason and drop the path.
var errLockContention = errors.New("another evener plugin operation is in progress")

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
// The returned release also reports this session's accumulated StoreChanged
// (store_changed.go) to the installed OnStoreChanged callback, whether or not
// migrateMarketplaceNames itself failed, after the lock is released so a slow
// broadcast never serializes behind the file lock.
func (m *Manager) lockStore(ctx context.Context, acquire lockAcquirer, timeout time.Duration) (func(), error) {
	release, err := m.acquireStoreLock(ctx, acquire, m.lockPath(), timeout)
	if err != nil {
		return nil, m.lockFailed(err)
	}
	release = m.reportingRelease(release)
	if err := m.migrateMarketplaceNames(); err != nil {
		release()
		return nil, m.migrationFailedErr(err)
	}
	return release, nil
}

// lockClone takes the lock on the marketplace clone at dir and returns its
// release. A check fetches a clone with the store lock free
// (fastForwardMarketplace), so whatever else runs git in a clone or removes
// or renames one takes this too: two gits in one clone collide on its ref
// locks, and Windows refuses to delete or rename a directory a git is
// running in. It is a file lock, so it holds against other evener processes
// as the store lock does, keyed by the clone directory's name (every clone
// lives in the marketplaces directory). Taken after the store lock, never
// before it, and held for no store-lock wait. Its error carries the lock
// file's path only to the log, as lockFailed's does.
func (m *Manager) lockClone(ctx context.Context, dir string) (func(), error) {
	path, err := m.cloneLockPath(dir)
	if err != nil {
		return nil, err
	}
	release, err := acquireLockAtPath(ctx, path)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: taking the lock on marketplace clone %s: %v\n", dir, err)
		msg := fmt.Sprintf("the lock on marketplace clone %q could not be taken; see the hub's log for detail", filepath.Base(dir))
		if cause := editFailureIdentity(err); cause != nil {
			msg = fmt.Sprintf("%s: %v", msg, cause)
		}
		return nil, &lockAcquisitionError{msg: msg, cause: err}
	}
	return release, nil
}

// acquireLockAtPath takes the lock on the file at path, as it is when the
// lock is granted. removeCloneLock deletes a clone's lock file while holding
// it, so a waiter can be granted the lock on the deleted file while a later
// taker creates and locks a new one; each would think it held the lock. So
// the file at path is looked up before the wait and again once the lock is
// granted, and a lock on any other file is let go and taken again. The file
// is made first, so a first lock has one to compare and is taken once, and
// the passes share one 30s wait.
func acquireLockAtPath(ctx context.Context, path string) (func(), error) {
	deadline := lockNow().Add(30 * time.Second)
	if err := lockMkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating lock parent: %w", err)
	}
	f, err := lockOpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", path, err)
	}
	_ = f.Close()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before, err := os.Stat(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		remaining := deadline.Sub(lockNow())
		if remaining <= 0 {
			return nil, fmt.Errorf("%w (locked: %s)", errLockContention, path)
		}
		release, err := acquireLock(ctx, path, remaining)
		if err != nil {
			return nil, err
		}
		// A file removed and made again since the first look is another
		// taker's; the next pass compares afresh.
		if after, err := os.Stat(path); before != nil && err == nil && os.SameFile(before, after) {
			return release, nil
		}
		release()
	}
}

func (m *Manager) cloneLockPath(dir string) (string, error) {
	return m.storePath(cloneLocksDirName, filepath.Base(dir)+".lock")
}

// removeCloneLock removes the lock file of the clone at dir, whose lock the
// caller holds, once the clone itself is gone. Best effort: a file left
// behind is reused if the name comes back.
func (m *Manager) removeCloneLock(dir string) {
	if path, err := m.cloneLockPath(dir); err == nil {
		_ = os.Remove(path)
	}
}

// withClone runs op holding the lock on the clone at dir (lockClone).
func (m *Manager) withClone(ctx context.Context, dir string, op func() error) error {
	release, err := m.lockClone(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	return op()
}

// lockFailed scrubs a failed store-lock acquisition's absolute lock-file path -
// acquireLock and flockUntil build their errors around the lock path directly -
// before it reaches an RPC caller, logging the raw error server-side first, the
// way saveFailed scrubs a failed write. lockStore is where every writer takes
// the lock first, so this is the one place that text is turned into wire text.
// The reasons a caller still acts on survive as sentinels, path-free: lock
// contention (errLockContention, retry shortly), an unusable store root
// (errStoreRootUnset/errStoreRootNotAbsolute), and a cancellation or deadline.
//
// The raw acquisition error is kept as the result's cause, so errors.Is still
// reaches whatever the acquirer failed with - one of those sentinels, an fs
// error, or a test's own injected error - while the wire text names none of it.
//
// acquireStoreLock is also reached by Doctor's read-only walk, which does not
// come through here: Doctor reports locally, so naming the lock file it could
// not take is the point for its admin.
func (m *Manager) lockFailed(err error) error {
	_, _ = fmt.Fprintf(m.stderr(), "warning: taking the plugin store lock: %v\n", err)
	msg := "the plugin store lock could not be taken; see the hub's log for detail"
	// A refusal a caller has to act on - contention to retry, a store root to
	// fix, a cancellation - is named on the wire, path-free.
	if cause := editFailureIdentity(err); cause != nil {
		msg = fmt.Sprintf("%s: %v", msg, cause)
	}
	return &lockAcquisitionError{msg: msg, cause: err}
}

// lockAcquisitionError is lockFailed's result: path-free wire text laid over the
// raw acquisition error, which a caller reaches through Unwrap (so errors.Is
// still finds it) but never sees in the message (so the lock file's absolute
// path stays in the hub's log).
type lockAcquisitionError struct {
	msg   string
	cause error
}

func (e *lockAcquisitionError) Error() string { return e.msg }
func (e *lockAcquisitionError) Unwrap() error { return e.cause }

// reportingRelease wraps release so that, in order: this session's
// accumulated change is captured and cleared, the store lock is let go, and
// only then — outside the lock — the installed callback (if any) is told
// what changed. A session that changed nothing invokes no callback at all.
func (m *Manager) reportingRelease(release func()) func() {
	return func() {
		changed, cb := m.takeStoreChanged()
		release()
		if (changed.Plugins || changed.Marketplaces) && cb != nil {
			cb(changed)
		}
	}
}

// migrateStore takes the store lock for nothing but the migration lockStore
// runs on the way in, and releases it again. The sweeps (UpdateAll,
// UpdateAutoUpgrade) call this before they enumerate the registry, because
// the keys they enumerate have to postdate the migration: otherwise the first
// per-plugin upgrade's own lock performs it and the rest of the sweep asks
// for keys the rename has just replaced. The lock is released rather than
// held across those upgrades, each of which takes it itself.
func (m *Manager) migrateStore(ctx context.Context) error {
	release, err := m.lockStore(ctx, installAcquireLock, 30*time.Second)
	if err != nil {
		return err
	}
	release()
	return nil
}

// acquireBundledLock takes the bundled cache's lock for one mutation of
// <Root>/bundled. Every bundled-cache mutation acquires here and nowhere else,
// and it goes through acquireStoreLock so the store-root check lands on this
// lock the same way it lands on the store lock. Its failure is scrubbed through
// lockFailed too: PreviewForLaunch readies the bundled store, and the hub's
// plugin/preview copies that error onto its wire diagnostic, so the bundled
// lock file's absolute path must not be in it.
func (m *Manager) acquireBundledLock(ctx context.Context, timeout time.Duration) (func(), error) {
	release, err := m.acquireStoreLock(ctx, acquireLock, m.bundledLockPath(), timeout)
	if err != nil {
		return nil, m.lockFailed(err)
	}
	return release, nil
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
			return nil, fmt.Errorf("%w (locked: %s)", errLockContention, lockPath)
		}
		lockSleep(backoff)
		backoff *= 2
		if backoff > 200*time.Millisecond {
			backoff = 200 * time.Millisecond
		}
	}
}
