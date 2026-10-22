//go:build darwin

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
	return fmt.Sprintf("Darwin filesystem %s, mount flags %#x", unix.ByteSliceToString(stat.Fstypename[:]), stat.Flags), !darwinLocalMount(stat.Flags), nil
}

func darwinLocalMount(flags uint32) bool { return flags&unix.MNT_LOCAL != 0 }
