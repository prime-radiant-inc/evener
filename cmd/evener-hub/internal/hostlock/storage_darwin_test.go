//go:build darwin

package hostlock

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestStorageDarwinClassification(t *testing.T) {
	for _, test := range []struct {
		flags uint32
		local bool
	}{{0, false}, {unix.MNT_RDONLY, false}, {unix.MNT_LOCAL, true}, {unix.MNT_LOCAL | unix.MNT_RDONLY, true}} {
		if got := darwinLocalMount(test.flags); got != test.local {
			t.Errorf("flags=%#x admitted=%v want=%v", test.flags, got, test.local)
		}
	}
}
