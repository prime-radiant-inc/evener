//go:build linux

package execenv

import "golang.org/x/sys/unix"

// devtmpfsMagic is devtmpfs's superblock magic (the kernel's DEVTMPFS_MAGIC,
// fs/devtmpfs.c); x/sys does not define it.
const devtmpfsMagic = 0x966d657c

// fdOnPseudoMount reports whether fd's file lives on a pseudo-filesystem
// mount (procfs, sysfs, devtmpfs) — a descriptor on one can block in read(2)
// while statting regular, which is the read the streamed classification
// exists to interrupt. The mount magic describes the opened descriptor
// itself, so no alias the path was reached through can hide it. Tmpfs is
// deliberately absent: /dev/shm and the system's scratch directories are
// tmpfs-backed and read like ordinary files, and a statfs failure falls back
// to the caller's spelled check.
func fdOnPseudoMount(fd int) bool {
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return false
	}
	return st.Type == unix.PROC_SUPER_MAGIC ||
		st.Type == unix.SYSFS_MAGIC ||
		st.Type == devtmpfsMagic
}
