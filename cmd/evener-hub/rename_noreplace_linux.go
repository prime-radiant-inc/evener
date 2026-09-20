//go:build linux

package hub

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplaceAtomic moves src to dst with the kernel's own no-replace
// rename: renameat2's RENAME_NOREPLACE refuses a taken destination instead of
// replacing it, in the single syscall the move is. Nothing is checked before it,
// so there is no window between a check and the move for a writer to create the
// destination in - the refusal is the kernel's answer to this one call.
func renameNoReplaceAtomic(src, dst string) error {
	return unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
}

// renameNoReplaceAtomicUnsupported reports whether a no-replace rename failed
// because this filesystem has no such operation, rather than because the move
// failed: a network or FUSE mount answers EINVAL, and a kernel without
// renameat2 answers ENOSYS. The caller then uses its checked fallback, which is
// the move every filesystem can do, and says what that costs.
func renameNoReplaceAtomicUnsupported(err error) bool {
	return errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EINVAL)
}
