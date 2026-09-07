//go:build unix

package tui

import (
	"os"
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

// TestCredentialJsonResult_BoundsAFileThatGrowsAfterItIsOpened: the size is
// judged on the opened file and the read is bounded, so a file that is
// replaced or extended between the check and the read cannot get past the
// limit. Written as a file that reports one size and holds more, which is
// what such a race produces.
func TestCredentialJsonResult_BoundsAFileThatGrowsAfterItIsOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grows.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, maxCredentialFileBytes+4096)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	assertCredentialPathRefused(t, path, "too large")
}
