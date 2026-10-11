//go:build unix

package hub

import (
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO in the session's folder is refused without blocking: the open does
// not wait for a writer, and the descriptor's own stat says it is not a
// regular file. Were the open to block, this test would hang at its deadline.
func TestReadDocFile_RefusesAFIFOWithoutBlocking(t *testing.T) {
	root := docTestRoot(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}

	if read, err := readDocFile(root, fifo); err == nil {
		t.Fatalf("read %q from a FIFO, want a refusal", read.Data)
	}
}
