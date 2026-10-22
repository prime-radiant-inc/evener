//go:build !linux && !darwin

package hostlock

import (
	"errors"
	"os"
)

func lockOwnedByCurrentUser(os.FileInfo) bool { return false }
func flockInspectionSupported() bool          { return false }
func flockIsHeld(*os.File) (bool, error) {
	return false, errors.New("host lock inspection is unsupported on this platform")
}

// This platform has no flock primitive wired up, and evener-hub never ships
// here. AcquireLock's caller treats a lock failure as fatal at startup, so
// failing closed (rather than pretending to hold an exclusive lock we can't
// actually enforce) is the safe direction.
func flockExclusiveNonBlocking(f *os.File) error {
	return errors.New("host lock is unsupported on this platform")
}

func lockMarker(*os.File) error {
	return nil
}

func openStorageDirectory(path string) (*os.File, error) { return os.Open(path) }
