//go:build !unix

package selfupdate

import "errors"

// Restart is unsupported outside unix: there is no exec-in-place there.
func Restart(binary string, args []string) error {
	return errors.New("in-place restart is not supported on this platform")
}

// RestartSupported reports whether Restart can exec in place on this build.
func RestartSupported() bool { return false }
