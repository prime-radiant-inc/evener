//go:build unix

package plugins

import (
	"errors"
	"syscall"
)

// isNameTooLong reports whether err is the filesystem refusing a path because a
// component, or the whole path, is longer than it takes. Such a path names
// nothing that is there — the filesystem will not even look at it — so a caller
// asking what a path holds treats it as absent rather than as a failure.
func isNameTooLong(err error) bool {
	return errors.Is(err, syscall.ENAMETOOLONG)
}
