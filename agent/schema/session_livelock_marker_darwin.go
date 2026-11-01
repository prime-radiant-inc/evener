//go:build darwin

package schema

import "os"

// markLivelockHeld is a no-op on Darwin: the flock the mint takes is itself
// visible to the F_GETLK inspection SessionLivelockHeld uses, so no second
// marker is needed (the hostlock marker pattern's darwin arm).
func markLivelockHeld(*os.File) error {
	return nil
}
