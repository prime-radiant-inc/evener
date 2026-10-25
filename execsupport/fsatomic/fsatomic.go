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
	"sync"
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
// wraps ErrPublished. The parent directory must
// exist (MkdirAll creates it durably). A failed write removes its temp file; a crash between the create and
// the rename leaves a stray "<name>.tmp-*" file behind, which is the cost of
// never reusing a name another process could plant.
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

// unsyncedDirs holds directories this process created whose parent entry is
// not yet confirmed durable: a sync is in flight or failed. MkdirAll syncs them
// again before reporting success, so neither a concurrent caller that sees the
// directory early nor a retry after a failed sync skips the sync.
var (
	unsyncedMu   sync.Mutex
	unsyncedDirs = map[string]bool{}
)

func setUnsynced(dir string, unsynced bool) {
	unsyncedMu.Lock()
	defer unsyncedMu.Unlock()
	if unsynced {
		unsyncedDirs[dir] = true
	} else {
		delete(unsyncedDirs, dir)
	}
}

func isUnsynced(dir string) bool {
	unsyncedMu.Lock()
	defer unsyncedMu.Unlock()
	return unsyncedDirs[dir]
}

// MkdirAll creates dir and any missing ancestors like os.MkdirAll, but one
// component at a time, syncing each new directory's parent before creating the
// next, so the new directory entries are durable. Write syncs only the
// directory that holds the file, so a caller whose first save also creates the
// directory runs this before Write.
func MkdirAll(dir string, perm os.FileMode) error {
	var todo []string // innermost first: missing, or created but not yet synced
	for p := dir; ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		switch {
		case err == nil && !info.IsDir():
			return &os.PathError{Op: "mkdir", Path: p, Err: syscall.ENOTDIR}
		case err == nil && !isUnsynced(p):
			// An existing directory this process has not left unsynced:
			// everything above it was settled when it was created.
			return syncAll(todo, perm)
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		}
		todo = append(todo, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	return syncAll(todo, perm)
}

// syncAll creates (if missing) and syncs the directories in todo, outermost
// first.
func syncAll(todo []string, perm os.FileMode) error {
	for i := len(todo) - 1; i >= 0; i-- {
		p := todo[i]
		setUnsynced(p, true)
		if err := os.Mkdir(p, perm); err != nil {
			info, statErr := os.Stat(p)
			if !errors.Is(err, os.ErrExist) || statErr != nil || !info.IsDir() {
				setUnsynced(p, false)
				return err
			}
		}
		if err := syncDir(filepath.Dir(p)); err != nil {
			return err
		}
		setUnsynced(p, false)
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
