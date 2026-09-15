//go:build !linux && !darwin

package hubcore

import "primeradiant.com/evener/rendezvous"

// processAlive has no signal-0 equivalent on this platform via the os
// package, and evener-hub never ships here. A false result evicts the entry
// from the roster, so assuming alive is the safe direction: it costs a stale
// entry rather than silently dropping a live session.
func processAlive(pid int) bool {
	return true
}

// processIdentity: no generation-bound process inspection here, so liveness
// alone decides, as it always has.
func processIdentity(entry rendezvous.Entry) ProcessIdentity {
	if !processAlive(entry.PID) {
		return ProcessNotOwner
	}
	return ProcessIdentityUnknown
}
