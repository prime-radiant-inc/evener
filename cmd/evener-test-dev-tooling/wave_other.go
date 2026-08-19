//go:build !linux && !darwin

package main

import (
	"os"
	"syscall"
)

// This platform has no process groups; the tool only runs the repo's unix
// selftests, so these stand-ins just keep the build green (mirroring
// agent/execenv's platform seams). Both stop paths collapse to a best-effort
// kill of the direct child.

func suiteSysProcAttr() *syscall.SysProcAttr { return nil }

func terminateSuiteGroup(pgid int) { killSuiteGroup(pgid) }

func killSuiteGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	if proc, err := os.FindProcess(pgid); err == nil {
		_ = proc.Kill()
	}
}
