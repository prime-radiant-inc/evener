//go:build !unix

package agent

// processAlive cannot probe processes on this platform, so it conservatively
// reports every positive pid as alive: the owner-liveness oracle then keeps
// the lane locked (the safe direction) rather than guessing at death. None of
// these GOOS is a shipped target (see schema/snapshot_flock_other.go).
func processAlive(pid int) bool {
	return pid > 0
}
