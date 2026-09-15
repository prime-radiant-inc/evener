//go:build linux || darwin

package hubcore

import (
	"errors"
	"path/filepath"
	"syscall"

	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/envvars"
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

// processIdentity asks the same verifier force-stop binds to
// (daemonprocess.Controller): process generation, owner, a `serve` argv, a
// start no later than the rendezvous start time, and possession of the
// session's API log. Only positive evidence counts against the entry: the
// process is gone (ErrExited) or is verifiably another process
// (ErrNotDaemon). A verification that could not run or could not vouch - a
// pidfd that will not open, a /proc read racing a closing descriptor on a
// busy daemon, an entry a daemon wrote without the fields it needs - says
// unknown, and liveness alone decides as it always has.
func processIdentity(entry rendezvous.Entry) ProcessIdentity {
	target := daemonprocess.Target{
		PID:       entry.PID,
		SessionID: envvars.FirstNonEmpty(entry.SessionID, entry.ThreadID),
		StateDir:  entry.StateDir,
		StartedAt: entry.StartedAt,
	}
	if target.SessionID == "" || !filepath.IsAbs(target.StateDir) || target.StartedAt.IsZero() {
		return ProcessIdentityUnknown
	}
	process, err := daemonprocess.NewController().Open(target)
	if err != nil {
		if errors.Is(err, daemonprocess.ErrExited) || errors.Is(err, daemonprocess.ErrNotDaemon) {
			return ProcessNotOwner
		}
		return ProcessIdentityUnknown
	}
	_ = process.Close()
	return ProcessOwnsEntry
}
