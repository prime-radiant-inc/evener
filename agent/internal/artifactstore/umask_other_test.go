//go:build !unix

package artifactstore

// relaxUmask is a no-op where there is no process umask; the helper that needs
// it is skipped on those platforms anyway. See the unix build for why it exists.
func relaxUmask() {}
