//go:build windows

package sandbox

import (
	"fmt"
	"os"
)

// scratchModesRecorded is false on Windows: it does not record POSIX permission or
// sticky bits, Go synthesizes writable permission bits, and os.ModeSticky is never
// set — so a mode-based judgement would call every location replaceable and refuse
// to allocate scratch at all.
var scratchModesRecorded = false

// linkCount reports that the link count is unknown on Windows: os.FileInfo does not
// expose it here, and a mode change on this platform does not carry POSIX hard-link
// semantics, so the caller treats the file as singly linked.
func linkCount(os.FileInfo) (int, bool) { return 0, false }

// fileOwnerID reports that the owning uid is unknown on Windows: it records no POSIX
// modes, so no mode-based judgement depends on the owner here.
var fileOwnerID = func(os.FileInfo) (int, bool) { return 0, false }

// currentUserID has no POSIX meaning on Windows; the owner checks are gated on
// fileOwnerID reporting an owner at all.
func currentUserID() int { return 0 }

// openScratchDirNoFollow opens path as a directory without accepting a reparse
// point (symlink/junction) at its final component, so the fchmod that
// ensureScratchSubdir performs through this handle cannot be redirected outside
// the scratch. Windows has no O_NOFOLLOW equivalent in the os package, so the
// entry is checked before the handle is opened.
func openScratchDirNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("sandbox: open scratch subdirectory %q: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("sandbox: scratch subdirectory %q is not a directory", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("sandbox: open scratch subdirectory %q: %w", path, err)
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sandbox: stat scratch subdirectory %q: %w", path, err)
	}
	if !opened.IsDir() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("sandbox: scratch subdirectory %q changed while opening", path)
	}
	return file, nil
}

// lockScratchDirFile is a no-op on Windows. The lock serializes Evener's writes inside
// a container whose exported modes only exist on POSIX; Windows records none of them
// (see scratchModesRecorded), so there is no setup window to serialize.
func lockScratchDirFile(*os.File, bool) error { return nil }

// tryLockScratchDirFile reports that no other holder remains, the same answer the
// POSIX probe gives when no consumer is sharing the directory.
func tryLockScratchDirFile(*os.File) (bool, error) { return true, nil }

// unlockScratchDirFile is the no-op counterpart of lockScratchDirFile.
func unlockScratchDirFile(*os.File) error { return nil }
