//go:build linux || darwin

package plugins

import (
	"errors"

	"golang.org/x/sys/unix"
)

// The flock(2) primitive behind acquireLock. lockFlock stays a seam so tests
// can script contention and failures without a real lock file.
var lockFlock = unix.Flock

const (
	lockOpExclusiveNB = unix.LOCK_EX | unix.LOCK_NB
	lockOpUnlock      = unix.LOCK_UN
)

// isLockContended reports whether a flock attempt failed only because another
// process holds the lock (retryable), as opposed to a genuine error.
func isLockContended(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
