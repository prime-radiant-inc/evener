//go:build !unix || aix || solaris

package githubcopilot

import "errors"

// errLockUnsupported reports that this platform has no cross-process record
// lock: the standard library has no flock on aix or solaris, and Evener ships
// no other non-Unix target. This file keeps cross-compilation working without
// claiming credential coordination on those platforms.
var errLockUnsupported = errors.New("GitHub Copilot auth record locking is not supported on this platform")

func lockRecordFile(string) (func() error, error) {
	return nil, errLockUnsupported
}
