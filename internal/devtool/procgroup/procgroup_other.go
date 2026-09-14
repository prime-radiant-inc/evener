//go:build !linux && !darwin

// This platform has no process groups; the dev tooling only runs on the
// repo's unix development machines, so these stand-ins just keep the build
// green. Both stop paths collapse to a best-effort kill of the direct child.
package procgroup

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Supported is false here: there are no process groups to stop.
const Supported = false

func Start(cmd *exec.Cmd) error { return cmd.Start() }

func Terminate(pgid int) { Kill(pgid) }

func Kill(pgid int) {
	if pgid <= 0 {
		return
	}
	if proc, err := os.FindProcess(pgid); err == nil {
		_ = proc.Kill()
	}
}

// Exists answers no: without process groups there is no group to outlive the
// child, and the caller's Wait has already accounted for that child.
func Exists(int) bool { return false }

// Stop reports whether the child was stopped by this call, the same question
// the unix build answers: false when there was nothing to signal. The error is
// what the kill refused with, and os.Process.Kill does not distinguish a
// process that was already gone, so a stop here never reports one.
func Stop(pgid int, reaped <-chan struct{}, grace time.Duration) (bool, error) {
	if pgid <= 0 {
		return false, nil
	}
	select {
	case <-reaped:
		return false, nil
	default:
	}
	Terminate(pgid)
	select {
	case <-reaped:
	case <-time.After(grace):
	}
	return true, nil
}

// StopWith has no signal to forward on a platform with one way to stop a
// process, so it is Stop.
func StopWith(pgid int, _ syscall.Signal, reaped <-chan struct{}, grace time.Duration) (bool, error) {
	return Stop(pgid, reaped, grace)
}

func ExitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
