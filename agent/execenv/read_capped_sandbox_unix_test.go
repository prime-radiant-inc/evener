//go:build linux || darwin

package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"primeradiant.com/evener/agent/sandbox"
)

// TestSandboxReadFileCapsWholeFileRead pins the byte cap on the fd-anchored
// arm: a regular file past MaxWholeFileReadBytes fails with the limit refusal
// instead of loading whole. Regular-only admission cannot bound this read by
// itself — a procfs regular such as /proc/kcore stats in the terabytes — so the
// read itself must stop at the cap (issue #4556). The fixture is sparse, so the
// refusal costs no I/O beyond the capped read.
func TestSandboxReadFileCapsWholeFileRead(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	path := filepath.Join(worktree, "oversize.log")
	sparseOversize(t, path)
	got, err := s.readFile("read_file", path)
	if err == nil {
		t.Fatalf("readFile(oversize) = %d bytes, nil; want the read-limit refusal", len(got))
	}
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("readFile(oversize) err = %v, want a *ReadLimitError", err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
}

// A regular the classification routes as a stream can block in read(2)
// although it stats regular — the aliased kmsg shape, canonical root-only —
// and the file-tools read has no context to observe, so the stall deadline
// must bound it in time (#4574 round 10 ruling): a never-ready stream fails
// at grepReadStallTimeout instead of holding the read forever. A FIFO under
// a spelled-pseudo root with a writer that never writes stands in for the
// root-only regular: the admission stub bypasses only the type gate, handing
// back the fd with the flag the real admission leaves per its streamed
// parameter, while the open, the classification, and the read all run the
// real code. The writer stays open through the read attempt — with no writer
// at all a FIFO read answers EOF, never a park — and its cleanup close is
// what lets a pre-fix parked read goroutine exit after the test.
func TestSandboxReadFileBoundsAStalledStreamRead(t *testing.T) {
	// Not parallel: the test swaps admitReadFD, pseudoFsRoots, and
	// grepReadStallTimeout.
	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	stalled := filepath.Join(worktree, "kmsg-shaped")
	if err := unix.Mkfifo(stalled, 0o600); err != nil {
		t.Fatal(err)
	}
	// A nonblocking read side so the writer can open — a write-only open
	// needs a reader (ENXIO otherwise), and a blocking read-only open would
	// itself wait for the writer.
	rfd, err := unix.Open(stalled, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	testReader := os.NewFile(uintptr(rfd), stalled)
	defer func() { _ = testReader.Close() }()
	wfd, err := unix.Open(stalled, unix.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer := os.NewFile(uintptr(wfd), stalled)
	defer func() { _ = writer.Close() }()
	origAdmit := admitReadFD
	admitReadFD = func(fd int, name string, allowDir, streamed bool) (*os.File, error) {
		f := os.NewFile(uintptr(fd), name)
		if !streamed {
			if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0); err == nil {
				_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags&^unix.O_NONBLOCK)
			}
		}
		return f, nil
	}
	t.Cleanup(func() { admitReadFD = origAdmit })
	stubPseudoFsRoots(t, worktree)
	origStall := grepReadStallTimeout
	grepReadStallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { grepReadStallTimeout = origStall })

	done := make(chan error, 1)
	go func() {
		_, err := s.readFile("read_file", stalled)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errGrepStreamStalled) {
			t.Fatalf("readFile on a never-ready pseudo-rooted regular = %v, want the stall error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readFile on a never-ready pseudo-rooted regular never returned: the unbounded read the stall deadline exists to bound")
	}
}

// Not parallel: the allocation measurement must exclude sibling test activity.
// TestSandboxReadFileRefusesOversizeBeforeReadingIt pins the fd-anchored arm's
// refusal at the stat size: an oversized regular file fails before a byte is
// read, matching the off arm's fstat refusal. Reading to the cap first would
// churn the whole buffer for every such read (issue #4556).
func TestSandboxReadFileRefusesOversizeBeforeReadingIt(t *testing.T) {
	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	path := filepath.Join(worktree, "oversize.log")
	sparseOversize(t, path)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := s.readFile("read_file", path)
	runtime.ReadMemStats(&after)
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("readFile(oversize) err = %v, want the read-limit refusal", err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("refusing an oversize file allocated %d bytes, want the refusal before any read", allocated)
	}
}
