//go:build linux || darwin

package execenv

import (
	"errors"
	"os"
	"path/filepath"
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
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create oversize: %v", err)
	}
	if err := os.Truncate(path, MaxWholeFileReadBytes+1); err != nil {
		t.Fatalf("truncate oversize: %v", err)
	}
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
