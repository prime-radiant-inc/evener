//go:build unix

package plugins

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO in an archived tree or a prepared payload is not content this code
// can copy: the copy must refuse it rather than block on the open.
func TestCopyRefusesFIFOEntries(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(src, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyEntryExact(fifo, filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("a FIFO archive entry was accepted")
	}
	if err := copyTreeExact(src, filepath.Join(t.TempDir(), "copy")); err == nil {
		t.Fatal("a FIFO in prepared content was accepted")
	}
}
