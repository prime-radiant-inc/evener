//go:build unix

// Package proclive reports whether a pid names a live process.
package proclive

import (
	"errors"
	"os"
	"syscall"
)

// Alive reports whether pid names a live process. A signal-0 probe answers nil
// for a live process and ESRCH for a gone one; EPERM means the process exists
// but is not signalable by this one, which is liveness.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}
