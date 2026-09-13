//go:build unix

package dev

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts the child in a process group of its own, so that
// stopProcessGroup can reach everything it forks.
func isolateProcessGroup(cmd *exec.Cmd) bool {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return true
}

// stopProcessGroup signals the whole group the child leads: politely first,
// then, once the grace has passed, not. The child is the group leader, so the
// group id is its pid.
func stopProcessGroup(cmd *exec.Cmd, force bool) {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	_ = syscall.Kill(-cmd.Process.Pid, signal)
}
