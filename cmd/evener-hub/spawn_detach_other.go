//go:build !linux && !darwin

package main

import "syscall"

// daemonSysProcAttr is nil where setsid is unsupported; daemons there keep
// the platform's default grouping.
func daemonSysProcAttr() *syscall.SysProcAttr { return nil }
