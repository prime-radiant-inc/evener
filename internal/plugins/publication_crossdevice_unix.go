//go:build unix

package plugins

import (
	"errors"
	"syscall"
)

// isCrossDevice reports whether a rename failed because source and destination
// are on different filesystems.
func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }
