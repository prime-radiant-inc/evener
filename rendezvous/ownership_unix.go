//go:build linux || darwin

package rendezvous

import (
	"fmt"
	"os"
	"syscall"
)

// ownershipFlock is the syscall seam behind withOwnershipLock. Tests replace
// it to observe and pace LOCK_EX acquisitions; production is syscall.Flock.
var ownershipFlock = syscall.Flock

// StrongOwnershipAvailable reports whether rendezvous writes and removals are
// serialized by a kernel lock on this platform. The daemon refuses ownership
// claims (manual retire, ownership-checked removal contracts) it cannot
// uphold where this is false.
func StrongOwnershipAvailable() bool { return true }

// withOwnershipLock runs fn holding LOCK_EX on the persistent <pid>.lock
// inode. The inode is deliberately never deleted: deleting it would orphan a
// lock held by a concurrent opener onto a stale inode and silently break
// mutual exclusion. Blocking flock (not LOCK_NB) so a replacement's write
// queues behind a stale remove instead of failing spuriously.
func withOwnershipLock(dir string, pid int, fn func() error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create rendezvous dir: %w", err)
	}
	f, err := os.OpenFile(ownershipLockPath(dir, pid), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open ownership lock: %w", err)
	}
	defer f.Close()
	if err := ownershipFlock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire ownership lock: %w", err)
	}
	defer func() { _ = ownershipFlock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}
