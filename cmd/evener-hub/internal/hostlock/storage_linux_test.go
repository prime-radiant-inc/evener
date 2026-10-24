//go:build linux

package hostlock

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestStorageLinuxClassification(t *testing.T) {
	for _, kind := range []uint64{unix.NFS_SUPER_MAGIC, unix.CIFS_SUPER_MAGIC, unix.SMB_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC, unix.AFS_SUPER_MAGIC, unix.AFS_FS_MAGIC, unix.CODA_SUPER_MAGIC, unix.CEPH_SUPER_MAGIC, unix.NCP_SUPER_MAGIC} {
		if name, rejected := linuxFilesystem(kind); !rejected || name == "" {
			t.Errorf("network %#x admitted or unclassified", kind)
		}
	}
	for _, kind := range []uint64{unix.EXT4_SUPER_MAGIC, unix.FUSE_SUPER_MAGIC, unix.OVERLAYFS_SUPER_MAGIC, unix.TMPFS_MAGIC, 0x12345678} {
		if _, rejected := linuxFilesystem(kind); rejected {
			t.Errorf("type %#x refused", kind)
		}
	}
}
