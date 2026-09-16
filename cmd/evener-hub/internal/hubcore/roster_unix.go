//go:build linux || darwin

package hubcore

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/rendezvous"
)

// processAlive reports whether a process with the given PID currently exists.
// Hub-spawned daemons run on the same host, so signal 0 is a reliable presence
// check; EPERM (the process exists but is owned by another user) counts as
// alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processIdentity is ProcessIdentity's unix reading (see that type for why the
// roster asks): a dead process is NotOwner; a live one is judged by
// daemonprocess.Identify. The hub is never a daemon, so a file naming the hub's
// own PID is a stale one whose PID the hub reused. An entry without the fields
// verification needs answers Unknown.
func processIdentity(entry rendezvous.Entry) ProcessIdentity {
	// Positive evidence that needs nothing from the entry comes first: the
	// hub's own PID, and a process that is gone. A legacy entry naming
	// either, without the fields verification needs, must not read as
	// unknown and stay parked.
	if entry.PID == os.Getpid() || !processAlive(entry.PID) {
		return ProcessNotOwner
	}
	target := DaemonTarget(entry)
	if target.SessionID == "" || !filepath.IsAbs(target.StateDir) || target.StartedAt.IsZero() {
		return ProcessIdentityUnknown
	}
	switch identity, _ := daemonprocess.Identify(target); identity {
	case daemonprocess.IdentityOwner:
		return ProcessOwnsEntry
	case daemonprocess.IdentityNotOwner:
		return ProcessNotOwner
	default:
		return ProcessIdentityUnknown
	}
}
