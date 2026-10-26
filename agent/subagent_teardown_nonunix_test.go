//go:build !unix

package agent

import "testing"

// scratchLeaseHeld has no flock to probe off unix: the scratch lease is a
// unix file lock, so there is nothing a lease check could observe here.
func scratchLeaseHeld(t *testing.T, dir string) bool {
	t.Helper()
	return false
}
