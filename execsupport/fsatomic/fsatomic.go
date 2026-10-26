// Package fsatomic publishes a file's new contents atomically and durably: a
// reader sees the old file or the complete new one, and once Write returns the
// new contents survive a crash.
package fsatomic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// ErrPublished marks a Write error that happened after the rename: the new
// contents are already visible at path and only the directory sync that makes
// them durable failed. A caller that treats a failed Write as "nothing
// changed" must check for it, and either reconcile with the published file or
// accept it.
var ErrPublished = errors.New("contents published but not confirmed durable")

// Write publishes data at path with permission perm. It writes a uniquely
// named temp file in path's directory (created exclusively, so a symlink
// planted at a predictable name is never followed), fsyncs it, sets perm
// outright so the process umask has no say, renames it over path, and fsyncs
// the directory so the rename itself is durable; a failure of that last step
// wraps ErrPublished.
//
// The parent directory must exist. Write makes the file durable in that
// directory; it does not sync the entries of directories the caller had to
// create to get there. A failed write removes its
// temp file; a crash between the create and the rename leaves a stray "<name>.tmp-*" file behind, which
// is the cost of never reusing a name another process could plant.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tempPath := temp.Name()
	published := false
	defer func() {
		if !published {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil && !SyncUnsupported(err) {
		_ = temp.Close()
		return fmt.Errorf("sync temp file for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename temp file over %s: %w", path, err)
	}
	published = true
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("%w: %w", ErrPublished, err)
	}
	return nil
}

// Remove deletes the file at path and syncs its directory so the deletion is
// durable. A file that is already gone is not an error, but its directory is
// still synced: the earlier attempt may have removed it and failed to sync.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		// Windows cannot flush a directory through a handle; NTFS journals
		// the rename itself.
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil && !SyncUnsupported(err) {
		_ = d.Close()
		return fmt.Errorf("sync directory %s: %w", dir, err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close directory %s: %w", dir, err)
	}
	return nil
}

// SyncUnsupported reports whether a file or directory Sync failed because the
// filesystem cannot sync it at all: the operation is not implemented (ENOSYS),
// not supported (ENOTSUP), or rejected outright (EINVAL, which some
// filesystems answer a directory sync with). The atomic-rename idiom tolerates
// exactly these failures: failing the whole store because the filesystem has
// no sync would turn a durability nicety into a hard outage. Every other sync
// failure is a real one the caller reports.
func SyncUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EINVAL)
}
