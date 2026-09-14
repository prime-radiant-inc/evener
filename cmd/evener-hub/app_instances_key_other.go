//go:build !unix && !windows

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

// createEndpointFingerprintKey creates the key file at path for writing. No
// O_NOFOLLOW exists on this platform, so a link at the path is not refused by
// the open; the repair writes only a temp name it has just generated, and the
// publish step judges the path it replaces (see publishFreshEndpointFingerprintKey).
func createEndpointFingerprintKey(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// lockEndpointFingerprintKey is a no-op on this platform: it has no advisory
// inter-process file lock, so nothing serializes the repair against another hub
// process sharing the state root - two of them can still each publish a key,
// and the last write wins. All this platform can offer is the in-process
// endpointFingerprintKeyMu and the re-read before a replacement.
func lockEndpointFingerprintKey(path string) (func(), error) {
	return func() {}, nil
}
