//go:build linux || darwin

package execenv

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

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
