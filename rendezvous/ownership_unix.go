//go:build linux || darwin

package rendezvous

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

// ownershipFlock is the syscall seam behind withOwnershipLock. Tests replace
// it to observe and pace LOCK_EX acquisitions; production is syscall.Flock.
// It is read on every rendezvous write/remove, which can run on a goroutine
// outliving the test that set it, so the seam is synchronized: a plain read
// racing a test's plain write is a data race under the Go memory model, and
// this repository runs -race.
var (
	ownershipFlockMu sync.Mutex
	ownershipFlock   = syscall.Flock
)

// setOwnershipFlock installs fn as the flock seam and returns a function that
// restores the previous value. Tests pair it with t.Cleanup; production never
// calls it.
func setOwnershipFlock(fn func(fd int, how int) error) func() {
	ownershipFlockMu.Lock()
	prev := ownershipFlock
	ownershipFlock = fn
	ownershipFlockMu.Unlock()
	return func() {
		ownershipFlockMu.Lock()
		ownershipFlock = prev
		ownershipFlockMu.Unlock()
	}
}

// installedOwnershipFlock returns the flock seam in effect for one lock
// acquisition. Taking it once keeps a single call's lock/unlock pair on the
// same function even if a test swaps the seam concurrently.
func installedOwnershipFlock() func(fd int, how int) error {
	ownershipFlockMu.Lock()
	defer ownershipFlockMu.Unlock()
	return ownershipFlock
}

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
	defer func() { _ = f.Close() }()
	flock := installedOwnershipFlock()
	if err := flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire ownership lock: %w", err)
	}
	defer func() { _ = flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}
