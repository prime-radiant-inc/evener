//go:build !linux && !darwin

package rendezvous

// StrongOwnershipAvailable reports whether rendezvous writes and removals are
// serialized by a kernel lock on this platform. Without flock there is no
// inter-process mutual exclusion here, so this platform cannot uphold the
// exact-ownership contract.
func StrongOwnershipAvailable() bool { return false }

// withOwnershipLock runs fn without serialization: no locking primitive
// exists on this platform. This preserves the pre-ownership behavior, and
// StrongOwnershipAvailable lets callers refuse contracts that require the
// strong form.
func withOwnershipLock(dir string, pid int, fn func() error) error {
	return fn()
}
