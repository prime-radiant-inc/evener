//go:build linux || darwin

package execenv

import "syscall"

func detachedProcessSysProcAttr() (*syscall.SysProcAttr, bool) {
	return &syscall.SysProcAttr{Setsid: true}, true
}
