// Package fsdurability holds the one predicate the hub's atomic-rename stores
// share: which sync failures mean "this filesystem cannot sync at all"
// rather than "the sync failed".
package fsdurability

import (
	"errors"
	"syscall"
)

// SyncUnsupported reports whether a directory (or file) Sync failed because
// the filesystem cannot sync it at all: the operation is not implemented
// (ENOSYS), not supported (ENOTSUP), or rejected outright (EINVAL, which some
// filesystems answer a directory sync with). The atomic-rename durability
// idiom — temp file, fsync, rename, then fsync the directory entry the rename
// landed in — tolerates exactly these failures at the directory-sync step:
// some filesystems cannot sync a directory at all, and failing the whole
// store there would turn a durability nicety into a hard outage. Every other
// sync failure is a real one the caller reports.
func SyncUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EINVAL)
}
