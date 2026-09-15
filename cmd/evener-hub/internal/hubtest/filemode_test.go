package hubtest

import (
	"os"
	"path/filepath"
	"testing"
)

// The stand-down is the helper's whole point, and only a Windows run reaches it
// through the real platform - which a host that records modes never does.
// Driving the seam here proves the path on any host: with the seam reporting no
// mode, a 0644 file must pass, and a helper that ignored the "judged" answer
// (or read the mode itself instead of the seam) would fail on it instead.
func TestAssertFileMode0600_StandsDownWhereNoModeIsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-owner-only")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	prev := fileModeSeam
	t.Cleanup(func() { fileModeSeam = prev })
	fileModeSeam = func(os.FileInfo) (os.FileMode, bool) { return 0, false }
	AssertFileMode0600(t, info, "a file on a platform that records no mode")
}
