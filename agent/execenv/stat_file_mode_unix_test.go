//go:build linux || darwin

package execenv

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// The confined walk derives a file's type from its stat mode, so only a
// regular file reports IsRegular.
func TestStatFileModeTypes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		raw     uint32
		regular bool
	}{
		{"regular", unix.S_IFREG | 0o644, true},
		{"fifo", unix.S_IFIFO | 0o600, false},
		{"socket", unix.S_IFSOCK | 0o600, false},
		{"char device", unix.S_IFCHR | 0o600, false},
		{"block device", unix.S_IFBLK | 0o600, false},
		{"symlink", unix.S_IFLNK | 0o777, false},
		{"directory", unix.S_IFDIR | 0o755, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mode := statFileMode(tc.raw)
			if mode.IsRegular() != tc.regular {
				t.Fatalf("IsRegular=%v want %v (mode %v)", mode.IsRegular(), tc.regular, mode)
			}
			if mode.Perm() != os.FileMode(tc.raw&0o777) {
				t.Fatalf("Perm=%v want %o", mode.Perm(), tc.raw&0o777)
			}
		})
	}
}
