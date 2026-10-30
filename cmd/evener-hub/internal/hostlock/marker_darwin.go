//go:build darwin

package hostlock

import "os"

// Darwin's flock itself is visible to F_GETLK, no second lock is needed.
func lockMarker(*os.File) error { return nil }
