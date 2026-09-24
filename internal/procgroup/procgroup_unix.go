//go:build linux || darwin

package procgroup

import "syscall"

// ProcessGroupsSupported reports whether this build can contain a
// spawned command's whole tree in one process group. The command
// expression evaluator refuses to run commands where it cannot (spec
// §10.1): a deadline kill that reaches only the direct child would
// leave the run's bounds a fiction.
var ProcessGroupsSupported = true

// SysProcAttr places a spawned command in its own process group so
// Terminate/Kill can signal the whole tree at once.
func SysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// Terminate signals the process group with SIGTERM.
func Terminate(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

// Kill signals the process group with SIGKILL.
func Kill(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// KillGroupAfterReap cleans up the process group after the direct child
// was reaped: the pid names no process the caller owns, but the group
// may still hold descendants holding captured pipes. On this platform
// that is the same group kill.
func KillGroupAfterReap(pid int) {
	Kill(pid)
}
