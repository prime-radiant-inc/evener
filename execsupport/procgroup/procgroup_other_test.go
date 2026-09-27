//go:build !linux && !darwin

package procgroup

import "testing"

// The post-reap cleanup must be inert here: the direct child is gone, the
// pid is free for reuse, and no group exists to reach, so a call naming
// any pid — the init process included — must neither panic nor signal.
func TestKillGroupAfterReapIsNoOp(t *testing.T) {
	KillGroupAfterReap(1)
}
