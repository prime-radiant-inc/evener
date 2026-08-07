//go:build !linux && !darwin

package execenv

import (
	"os"
	"syscall"
)

// This platform has no process groups or SIGTERM semantics. These definitions
// keep the build green (mirroring securepath_other.go's role): the spawned
// command runs without a dedicated process group, and both the graceful and
// forceful stop paths collapse to a best-effort kill of the direct child —
// erring toward killing too eagerly rather than leaving a process running.

func processGroupSysProcAttr() *syscall.SysProcAttr { return nil }

func terminateProcessGroup(pid int) { killDirectProcess(pid) }

func killProcessGroup(pid int) { killDirectProcess(pid) }

func killDirectProcess(pid int) {
	if pid <= 0 {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}
