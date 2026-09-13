//go:build unix

package dev

import (
	"errors"
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

// processGroupExists reports whether the group still has members. EPERM is a
// yes: the group is there, this process just may not signal all of it.
func processGroupExists(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
