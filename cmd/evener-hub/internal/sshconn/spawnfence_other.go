//go:build !linux && !darwin

package sshconn

// The non-Unix arm of §3's local process boundary: this platform offers no
// kernel-enforced boundary primitive, so a fenced spawn is refused rather than
// launched unowned. The hub's host-management paths are Unix-only in practice;
// this arm exists so the fence's contract is total and fail-closed everywhere.

import "fmt"

// defaultSpawnBoundary refuses: the boundary's whole guarantee is
// kernel-enforced membership, and a platform without one cannot provide it.
func defaultSpawnBoundary(root string) (SpawnBoundary, error) {
	_ = root
	return nil, fmt.Errorf("%w: this platform provides no local process boundary primitive", ErrSpawnBoundaryUnavailable)
}
