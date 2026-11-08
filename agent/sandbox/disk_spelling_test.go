package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDiskSpellingReportsTheFilesystemsOwnCase pins that a directory spelled in
// another letter case is reported as the filesystem stores it. A
// case-insensitive volume (ext4 casefold, vfat) resolves the path while the
// kernel reports the stored name; without this a mask or carve-out pinned to
// the configured spelling never matches the canonical path a backend sees.
// The test skips on a case-sensitive volume, where no path can differ.
func TestDiskSpellingReportsTheFilesystemsOwnCase(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("letter-case spelling is implemented only for linux and darwin")
	}
	parent := clean(t.TempDir())
	stored := filepath.Join(parent, "casehome")
	if err := os.MkdirAll(stored, 0o755); err != nil {
		t.Fatal(err)
	}
	spelled := filepath.Join(parent, "CASEHOME")
	if _, err := os.Stat(spelled); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	got, ok := diskSpelling(spelled)
	if !ok {
		t.Fatalf("diskSpelling(%q) reported no spelling", spelled)
	}
	if got != stored {
		t.Errorf("diskSpelling(%q) = %q, want the stored spelling %q", spelled, got, stored)
	}
}
