// Package hostlock provides a host-level exclusive lock so at most one
// serf-hub process runs per machine.
package hostlock

import (
	"fmt"
	"os"
	"path/filepath"
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
	if err := flockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		// Name the path (kata av1j): a raw errno carries none, and the losing
		// operator's remedy — find the holder, or isolate a test hub under its
		// own HOME so lock, run dir, state, and auth token all move together —
		// starts from knowing which lock file this was.
		return nil, fmt.Errorf("flock %s: %w (another serf-hub may already be running; a disposable hub needs its own HOME)", path, err)
	}
	return func() {
		_ = flockUnlock(f)
		_ = f.Close()
	}, nil
}
