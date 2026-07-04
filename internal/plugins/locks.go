package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// acquireLock takes an exclusive flock on lockPath, retrying with capped
// exponential backoff until timeout elapses. The returned release unlocks and
// closes the file.
func acquireLock(lockPath string, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating lock parent: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	deadline := time.Now().Add(timeout)
	backoff := 10 * time.Millisecond
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", lockPath, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another serf plugin operation is in progress (locked: %s)", lockPath)
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > 200*time.Millisecond {
			backoff = 200 * time.Millisecond
		}
	}
}
