//go:build unix

package tui

import (
	"path/filepath"
	"syscall"
	"testing"
)

// TestCredentialJsonResult_RefusesPipesAndDevices: the two paths that would
// not merely fail but hang or grow without bound — a pipe never returns, and
// a character device never ends — refused before the file is opened. Both
// are unix-only shapes, so they live apart from the portable cases.
func TestCredentialJsonResult_RefusesPipesAndDevices(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	t.Run("named pipe", func(t *testing.T) { assertCredentialPathRefused(t, fifo, "not a regular file") })
	t.Run("character device", func(t *testing.T) { assertCredentialPathRefused(t, "/dev/zero", "not a regular file") })
}
