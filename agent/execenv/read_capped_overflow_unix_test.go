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

// TestSandboxReadFileRefusesAStreamPastTheCap is item 1's regression: the
// sandboxed whole-file read routed through readBounded discarded readBounded's
// overflow signal, so an input whose stat size lies but whose read runs past
// the cap returned the first cap+1 bytes with err=nil — the merged #4556
// contract was to REFUSE with *ReadLimitError, and the off arm still refuses.
// The stand-in is a pipe, whose Stat reports size 0 while its read yields more
// than the (shrunken) cap, so the pre-stat size check passes and only the
// overflow path can answer.
func TestSandboxReadFileRefusesAStreamPastTheCap(t *testing.T) {
	// Not parallel: shrinks MaxWholeFileReadBytes and swaps admitReadFD.
	origLimit := MaxWholeFileReadBytes
	MaxWholeFileReadBytes = 8
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
	go func() {
		_, _ = pw.Write(make([]byte, int(MaxWholeFileReadBytes)+1))
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
