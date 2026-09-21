//go:build linux || darwin

package procgroup

import "syscall"

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
