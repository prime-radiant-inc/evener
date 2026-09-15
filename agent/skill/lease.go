package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// skillsLockDirName holds one lock file per cache directory. Locks live beside
// the directories rather than inside them, so a published copy stays exactly the
// embedded content and the digest walk sees nothing extra.
const skillsLockDirName = ".locks"

// skillsLease is a held lock on one cache directory. A reader holds a shared
// lease for as long as it may read that copy; the reaper must take an exclusive
// lease before removing anything, so a copy a live process is using is never
// removed.
type skillsLease interface {
	Release() error
	// Valid reports whether the lease still guards the path it was taken on. A
	// lock file another process unlinked and recreated leaves the lease on a
	// dead inode, which must not be trusted.
	Valid() bool
}

// acquireSkillsLease opens path (creating it) and takes a shared lock, or an
// exclusive one when exclusive is true. contended reports that the lock could
// not be taken, or that the lock file was replaced underneath it. It is a
// variable so tests can substitute a lease without a real lock file.
var acquireSkillsLease = platformAcquireSkillsLease

// skillsLockPath returns the lock file that guards the cache directory named
// name inside base, creating the lock directory first when create is set.
func skillsLockPath(base, name string, create bool) (string, error) {
	locks := filepath.Join(base, skillsLockDirName)
	if create {
		if err := os.MkdirAll(locks, 0o700); err != nil {
			return "", fmt.Errorf("creating skill lock dir: %w", err)
		}
	}
	return filepath.Join(locks, name+".lock"), nil
}

// tryExclusiveLease takes the exclusive lease at lockPath, reporting whether it
// succeeded. A contended, stale, or failed lease means the directory must not be
// removed.
func tryExclusiveLease(lockPath string) (skillsLease, bool) {
	lease, contended, err := acquireSkillsLease(lockPath, true)
	if contended || err != nil {
		if lease != nil {
			_ = lease.Release()
		}
		return nil, false
	}
	return lease, true
}

// pruneObsoleteLocks removes lock files whose cache directory no longer exists.
// It only unlinks a lock file it holds exclusively and that still names the file
// it locked, and only once the file is old enough that no publish is mid-rename,
// so it cannot drop a lease that another process is about to take.
func pruneObsoleteLocks(base string, now time.Time) {
	locks := filepath.Join(base, skillsLockDirName)
	entries, err := os.ReadDir(locks)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name, ok := cutLockFileName(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < staleStagingMaxAge {
			continue
		}
		if _, err := os.Lstat(filepath.Join(base, name)); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		path := filepath.Join(locks, entry.Name())
		lease, ok := tryExclusiveLease(path)
		if !ok {
			continue
		}
		// Re-check under the lease: a publisher can create the cache directory
		// between the check above and the lock, and unlinking its guard after
		// that would leave the new copy unprotected.
		if _, err := os.Lstat(filepath.Join(base, name)); !errors.Is(err, fs.ErrNotExist) {
			_ = lease.Release()
			continue
		}
		if info, err := os.Lstat(path); err != nil || now.Sub(info.ModTime()) < staleStagingMaxAge {
			_ = lease.Release()
			continue
		}
		_ = os.Remove(path)
		_ = lease.Release()
	}
}

// cutLockFileName returns the cache directory name a lock file guards.
func cutLockFileName(lockName string) (string, bool) {
	const suffix = ".lock"
	if len(lockName) <= len(suffix) || lockName[len(lockName)-len(suffix):] != suffix {
		return "", false
	}
	return lockName[:len(lockName)-len(suffix)], true
}
