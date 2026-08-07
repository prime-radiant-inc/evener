//go:build !linux && !darwin

package hostlock

import (
	"errors"
	"os"
)

// This platform has no flock primitive wired up, and serf-hub never ships
// here. AcquireLock's caller treats a lock failure as fatal at startup, so
// failing closed (rather than pretending to hold an exclusive lock we can't
// actually enforce) is the safe direction.
func flockExclusiveNonBlocking(f *os.File) error {
	return errors.New("host lock is unsupported on this platform")
}

func flockUnlock(f *os.File) error {
	return nil
}
