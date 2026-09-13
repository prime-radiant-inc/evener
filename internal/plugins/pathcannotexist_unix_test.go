//go:build unix

package plugins

import (
	"os"
	"syscall"
	"testing"
)

// A path the filesystem refuses to consider at all is one nothing can be at, so
// a probe reports it as absent instead of failing the operation that asked: the
// migration has to be able to look at a legacy name's directories to rename it,
// and one the filesystem will not name has none.
func TestPathPresent_APathThatCannotExistIsAbsent(t *testing.T) {
	cases := []struct {
		what string
		err  error
	}{
		{"a name too long", syscall.ENAMETOOLONG},
		{"a component that is not a directory", syscall.ENOTDIR},
		{"a name carrying a byte it cannot", syscall.EINVAL},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			origStat, origLstat := marketplaceStat, marketplaceLstat
			t.Cleanup(func() { marketplaceStat, marketplaceLstat = origStat, origLstat })
			fail := func(string) (os.FileInfo, error) {
				return nil, &os.PathError{Op: "stat", Path: "x", Err: tc.err}
			}
			marketplaceStat, marketplaceLstat = fail, fail

			present, err := pathPresent("x")
			if err != nil {
				t.Fatalf("pathPresent: %v", err)
			}
			if present {
				t.Fatalf("pathPresent = true, want a path the filesystem will not consider counted as absent")
			}
		})
	}
}
