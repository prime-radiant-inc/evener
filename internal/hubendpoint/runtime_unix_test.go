//go:build linux || darwin

package hubendpoint

import (
	"os"
	"syscall"
	"testing"
)

type otherOwnerRecordInfo struct {
	os.FileInfo
	uid uint32
}

func (f otherOwnerRecordInfo) Sys() any { return &syscall.Stat_t{Uid: f.uid} }
func TestEndpointRecordRejectsOtherOwner(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ownedByCurrentUser(otherOwnerRecordInfo{FileInfo: info, uid: uint32(os.Geteuid()) + 1}) {
		t.Fatal("accepted another OS user")
	}
}
