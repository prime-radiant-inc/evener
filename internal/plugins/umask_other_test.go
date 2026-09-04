//go:build !unix

package plugins

import "testing"

// pinPermissiveUmask skips the caller where there is no process umask and no
// POSIX permission bits to assert. See the unix build for what it does there.
func pinPermissiveUmask(t *testing.T) {
	t.Helper()
	t.Skip("no process umask on this platform")
}
