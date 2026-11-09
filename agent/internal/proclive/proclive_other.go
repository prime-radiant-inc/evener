//go:build !unix

package proclive

// Alive cannot probe processes on this platform, so it conservatively reports
// every positive pid as alive: callers keep an owner's resources (the safe
// direction) rather than guessing at death. None of these GOOS is a shipped
// target (see schema/snapshot_flock_other.go).
func Alive(pid int) bool {
	return pid > 0
}
