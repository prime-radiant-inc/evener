//go:build linux || darwin

package execenv

import "syscall"

// processGroupSysProcAttr places a spawned command in its own process group so
// terminateProcessGroup/killProcessGroup can signal the whole tree at once.
func processGroupSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
