//go:build linux || darwin

package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// TestSandboxReadFileRefusesAStreamPastTheCap pins that the sandboxed
// whole-file read refuses with *ReadLimitError, the same refusal the off arm
// returns, when an input's stat size understates what its read yields. The
// stand-in is a pipe. The cap sits far above any pipe buffer, so the stat size
// (0 on Linux; the buffered byte count on darwin, at most 64KiB) never trips
// the pre-read size check, and only the read's overflow signal can refuse.
func TestSandboxReadFileRefusesAStreamPastTheCap(t *testing.T) {
	// Not parallel: changes MaxWholeFileReadBytes and swaps admitReadFD.
	origLimit := MaxWholeFileReadBytes
	MaxWholeFileReadBytes = 1 << 20
	t.Cleanup(func() { MaxWholeFileReadBytes = origLimit })

	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	path := filepath.Join(worktree, "stream.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Sized on the test goroutine: the pipe gives the race detector no
	// ordering between a read of MaxWholeFileReadBytes in the writer and the
	// cleanup that restores it.
	pastTheCap := make([]byte, int(MaxWholeFileReadBytes)+1)
	go func() {
		_, _ = pw.Write(pastTheCap)
		_ = pw.Close()
	}()
	origAdmit := admitReadFD
	admitReadFD = func(fd int, name string, allowDir, streamed bool) (*os.File, error) {
		_ = unix.Close(fd)
		return pr, nil
	}
	t.Cleanup(func() { admitReadFD = origAdmit })

	got, err := s.readFile("read_file", path)
	if err == nil {
		t.Fatalf("readFile of a stream past the cap = %d bytes, nil; want the read-limit refusal", len(got))
	}
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("readFile past the cap err = %v, want a *ReadLimitError", err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
}
