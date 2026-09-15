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

// processIdentity is what the host can say about the process a rendezvous
// entry names: gone (signal 0 fails) is NotOwner; a process that answers is
// asked, through the verifier force-stop binds to, whether it is still the
// daemon that wrote the entry. The hub is never a daemon, so a file naming
// the hub's own PID is a stale one whose PID the hub reused. An entry a
// daemon wrote without the fields verification needs, or a host that cannot
// inspect, answers Unknown, and liveness alone decides as it always has.
func processIdentity(entry rendezvous.Entry) ProcessIdentity {
	if !processAlive(entry.PID) {
		return ProcessNotOwner
	}
	target := DaemonTarget(entry)
	if target.SessionID == "" || !filepath.IsAbs(target.StateDir) || target.StartedAt.IsZero() {
		return ProcessIdentityUnknown
	}
	if entry.PID == os.Getpid() {
		return ProcessNotOwner
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
