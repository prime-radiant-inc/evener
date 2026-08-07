//go:build !linux && !darwin

package plugins

import "errors"

// This platform has no flock(2). These definitions keep the build green; the
// lock attempt fails closed (never retried — isLockContended is always false)
// rather than pretending to hold a lock that cannot be enforced.
var lockFlock = func(fd int, how int) error {
	return errors.New("plugin locking is unsupported on this platform")
}

const (
	lockOpExclusiveNB = 0
	lockOpUnlock      = 0
)

func isLockContended(err error) bool { return false }
