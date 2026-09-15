//go:build linux || darwin

package hubcore

import (
	"errors"
	"syscall"
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
