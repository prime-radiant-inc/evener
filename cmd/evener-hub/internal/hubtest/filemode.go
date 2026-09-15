package hubtest

import (
	"os"
	"runtime"
	"testing"
)

// fileModeSeam reports the permission bits info records, and whether the
// platform records them at all. Windows synthesizes 0666 for every file (0444
// when it is read-only), so a comparison against the 0600 these packages write
// can never hold there and the assertion below would fail every Windows run.
// It is a var, not a func, so a test can drive the "no mode recorded" path on a
// host that does record modes - the endpointFingerprintKeyMode seam in package
// hub is the production twin of the same idea.
var fileModeSeam = func(info os.FileInfo) (os.FileMode, bool) {
	if runtime.GOOS == "windows" {
		return 0, false
	}
	return info.Mode().Perm(), true
}

// AssertFileMode0600 fails unless the file carries the owner-only 0600 mode
// these packages write to disk, on a platform that records permission bits at
// all: where the platform records none the assertion stands down rather than
// fail a run the file system cannot satisfy.
func AssertFileMode0600(t *testing.T, info os.FileInfo, what string) {
	t.Helper()
	perm, judged := fileModeSeam(info)
	if !judged {
		return
	}
	if perm != 0o600 {
		t.Errorf("%s is %o, want 0600", what, perm)
	}
}
