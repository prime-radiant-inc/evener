// Package hostlock provides a host-level exclusive lock so at most one
// serf-hub process runs per machine.
package hostlock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireLock takes an exclusive non-blocking flock on path. The returned
// release function closes the file (which drops the lock).
//
// Two hub processes on the same host must never run simultaneously: the
// rendezvous directory and the bind port would race. AcquireLock is the
// guard.
func AcquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir lock parent: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock: %w (another serf-hub may already be running)", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
