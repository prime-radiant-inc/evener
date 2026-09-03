//go:build unix

package plugins

import (
	"syscall"
	"testing"
)

// pinPermissiveUmask fixes the process umask at the conventional 0o022 for the
// rest of the test, so a test asserting an exact directory mode measures the
// mode this code asks for rather than the umask the suite happened to run
// under. Tests in this package never run in parallel, so the process-wide
// setting stays confined to the test that asks for it.
func pinPermissiveUmask(t *testing.T) {
	t.Helper()
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })
}
