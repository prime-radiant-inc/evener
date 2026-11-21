//go:build linux

package identifier

import "syscall"

// The superblock magics the pointer reader refuses. They are the kernel's own
// values (include/uapi/linux/magic.h); the syscall package defines none of
// them, and this package stays stdlib-only.
const (
	procSuperMagic = 0x9fa0     // PROC_SUPER_MAGIC
	sysfsMagic     = 0x62656572 // SYSFS_MAGIC
	devtmpfsMagic  = 0x966d657c // DEVTMPFS_MAGIC
)

// fdOnPseudoMount reports whether fd's file lives on a pseudo-filesystem mount
// (procfs, sysfs, devtmpfs) — a descriptor on one can block in read(2) while
// statting regular, which the pointer reader must refuse rather than read. The
// mount magic describes the opened descriptor itself, so no alias the path was
// reached through can hide it. Tmpfs is deliberately absent: /dev/shm and the
// system's scratch directories are tmpfs-backed and read like ordinary files,
// and a statfs failure falls back to the descriptor's own regular-file check.
func fdOnPseudoMount(fd int) bool {
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(fd, &st); err != nil {
		return false
	}
	return st.Type == procSuperMagic ||
		st.Type == sysfsMagic ||
		st.Type == devtmpfsMagic
}
