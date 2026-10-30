//go:build linux || darwin

package hostlock

import (
	"os"
	"syscall"
	"testing"
)

type otherOwnerLockInfo struct {
	os.FileInfo
	uid uint32
}

func (f otherOwnerLockInfo) Sys() any { return &syscall.Stat_t{Uid: f.uid} }
func TestEndpointIsHeldRejectsOtherOwner(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if lockOwnedByCurrentUser(otherOwnerLockInfo{FileInfo: info, uid: uint32(os.Geteuid()) + 1}) {
		t.Fatal("accepted another OS user")
	}
}
