//go:build linux || darwin

package main

import "syscall"

// suiteSysProcAttr places a suite in its own process group so a wave signal
// reaches every forked descendant, not just the script's shell.
func suiteSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func terminateSuiteGroup(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGTERM) }

func killSuiteGroup(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGKILL) }
