//go:build !linux && !darwin

package hubcore

// processAlive has no signal-0 equivalent on this platform via the os
// package, and serf-hub never ships here. A false result evicts the entry
// from the roster, so assuming alive is the safe direction: it costs a stale
// entry rather than silently dropping a live session.
func processAlive(pid int) bool {
	return true
}
