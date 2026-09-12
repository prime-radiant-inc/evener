//go:build unix

package plugins

import (
	"os"
	"syscall"
	"testing"
)

// A path the filesystem refuses for its length is one nothing can be at, so a
// probe reports it as absent instead of failing the operation that asked: the
// migration has to be able to look at a legacy name's directories to rename it,
// and one whose derived path is too long to name has none.
func TestPathPresent_ANameTooLongIsAbsent(t *testing.T) {
	origStat, origLstat := marketplaceStat, marketplaceLstat
	t.Cleanup(func() { marketplaceStat, marketplaceLstat = origStat, origLstat })
	tooLong := func(string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "stat", Path: "x", Err: syscall.ENAMETOOLONG}
	}
	marketplaceStat, marketplaceLstat = tooLong, tooLong

	present, err := pathPresent("x")
	if err != nil {
		t.Fatalf("pathPresent: %v", err)
	}
	if present {
		t.Fatal("pathPresent = true, want a path too long to name counted as absent")
	}
}
