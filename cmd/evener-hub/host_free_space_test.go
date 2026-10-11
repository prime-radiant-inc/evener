//go:build darwin || linux

package hub

// hostFreeSpaceTestCoverage is the smoke half of the free-space query's
// coverage: the unit the block counts are scaled by is a POSIX/statfs detail
// (Linux's fragment size, Darwin's block size), and no local test filesystem
// here carries a divergent pair to pin the choice against. This file pins the
// call's contract instead — a real directory answers a positive count, and a
// missing one is refused rather than reported as zero free.
import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostStateRootFreeSpaceAnswersRealDirectories(t *testing.T) {
	dir := t.TempDir()
	free, err := hostStateRootFreeSpace(dir)
	if err != nil {
		t.Fatalf("hostStateRootFreeSpace(%s): %v", dir, err)
	}
	if free == 0 {
		t.Fatalf("hostStateRootFreeSpace(%s) = 0, want the directory's available bytes", dir)
	}
	if _, err := hostStateRootFreeSpace(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("hostStateRootFreeSpace answered for a missing path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the probe mutated its input: %v", err)
	}
}
