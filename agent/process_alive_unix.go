//go:build unix

package agent

import "primeradiant.com/evener/agent/internal/proclive"

// processAlive reports whether pid names a live process. A signal-0 probe
// answers nil for a live process and ESRCH for a gone one; EPERM means the
// process exists but is not signalable by this one, which is liveness.
func processAlive(pid int) bool {
	return proclive.Alive(pid)
}
