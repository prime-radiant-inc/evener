package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type lockFile interface {
	Fd() uintptr
	Close() error
}

var (
	lockMkdirAll = os.MkdirAll
	lockOpenFile = func(name string, flag int, perm os.FileMode) (lockFile, error) {
		return os.OpenFile(name, flag, perm)
	}
	lockFlock = unix.Flock
	lockNow   = time.Now
	lockSleep = time.Sleep
)

// acquireLock takes an exclusive flock on lockPath, retrying with capped
// exponential backoff until timeout elapses. The returned release unlocks and
// closes the file.
func acquireLock(lockPath string, timeout time.Duration) (func(), error) {
	if err := lockMkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating lock parent: %w", err)
	}
	f, err := lockOpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	deadline := lockNow().Add(timeout)
	backoff := 10 * time.Millisecond
	for {
		err := lockFlock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = lockFlock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			_ = f.Close()
			return nil, fmt.Errorf("flock %s: %w", lockPath, err)
		}
		if lockNow().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("another serf plugin operation is in progress (locked: %s)", lockPath)
		}
		lockSleep(backoff)
		backoff *= 2
		if backoff > 200*time.Millisecond {
			backoff = 200 * time.Millisecond
		}
	}
}
