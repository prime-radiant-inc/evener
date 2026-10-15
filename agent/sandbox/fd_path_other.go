//go:build !darwin && !linux

package sandbox

import "errors"

// FdPath has no portable implementation elsewhere; callers treat the error as
// "cannot verify".
func FdPath(int) (string, error) {
	return "", errors.New("sandbox: no fd path lookup on this platform")
}
