//go:build !unix

package hub

import "os"

// openEndpointFingerprintKey opens the key file at path for reading. O_NOFOLLOW
// is not available on this platform, so the open follows a symlink at the path;
// the checks in readEndpointFingerprintKey judge - and read - what the path
// resolves to rather than the path again, which is what this platform can say
// about it.
func openEndpointFingerprintKey(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY, 0)
}
