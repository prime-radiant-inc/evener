//go:build !linux && !darwin

package procgroup

import (
	"os"
	"syscall"
)

// This platform has no process groups or SIGTERM semantics (mirroring the
// execenv fallback this package replaces): the spawned command runs without a
// dedicated process group, and both stop paths collapse to a best-effort kill
// of the direct child — erring toward killing too eagerly rather than leaving
// a process running.

func SysProcAttr() *syscall.SysProcAttr { return nil }

func Terminate(pid int) { killDirectProcess(pid) }

func Kill(pid int) { killDirectProcess(pid) }

func killDirectProcess(pid int) {
	if pid <= 0 {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}
