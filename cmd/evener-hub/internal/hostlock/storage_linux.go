//go:build linux

package hostlock

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func inspectFilesystem(file *os.File) (string, bool, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
		return "", false, err
	}
	name, nonlocal := linuxFilesystem(uint64(stat.Type))
	return name, nonlocal, nil
}

func linuxFilesystem(kind uint64) (string, bool) {
	switch kind {
	case unix.NFS_SUPER_MAGIC:
		return "NFS", true
	case unix.CIFS_SUPER_MAGIC:
		return "CIFS", true
	case unix.SMB_SUPER_MAGIC:
		return "SMB", true
	case unix.SMB2_SUPER_MAGIC:
		return "SMB2", true
	case unix.AFS_SUPER_MAGIC:
		return "AFS", true
	case unix.AFS_FS_MAGIC:
		return "AFS_FS", true
	case unix.CODA_SUPER_MAGIC:
		return "Coda", true
	case unix.CEPH_SUPER_MAGIC:
		return "Ceph", true
	case unix.NCP_SUPER_MAGIC:
		return "NCP", true
	default:
		return fmt.Sprintf("Linux filesystem type %#x, not identified as network storage", kind), false
	}
}
