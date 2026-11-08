// Package fsatomic publishes a file's new contents atomically and durably: a
// reader sees the old file or the complete new one, and once Write returns the
// new contents survive a crash.
package fsatomic

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// File is the open-file surface a durable replacement needs.
type File interface {
	io.Writer
	io.Closer
	Name() string
	Sync() error
}

// FS is the filesystem surface a durable replacement needs. WriteFS and
// SyncDirFS run over it, so an OS filesystem and an injected in-memory
// filesystem take the identical write/sync/rename sequence.
type FS interface {
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	Open(name string) (File, error)
	Rename(oldname, newname string) error
	Chmod(name string, mode os.FileMode) error
	Remove(name string) error
}

// Write publishes data at path with permission perm. It writes a uniquely
// named temp file in path's directory (created exclusively, so a symlink
// planted at a predictable name is never followed), checks the write length,
// fsyncs it, sets perm outright so the process umask has no say, renames it
// over path, and fsyncs the directory so the rename itself is durable; a
// failure of that last step wraps ErrPublished.
//
// The parent directory must exist. Write makes the file durable in that
// directory; it does not sync the entries of directories the caller had to
// create to get there. A failed write removes its
// temp file; a crash between the create and the rename leaves a stray "<name>.tmp-*" file behind, which
// is the cost of never reusing a name another process could plant.
func Write(path string, data []byte, perm os.FileMode) error {
	return WriteWithDirSync(path, data, perm, SyncDir)
}

// WriteWithDirSync is Write with the caller's own directory sync, for a store
// whose tests must fail the sync after the rename deterministically (a real
// filesystem cannot be made to fail one sync of several). A syncDir error
// wraps ErrPublished exactly as a real sync failure does.
func WriteWithDirSync(path string, data []byte, perm os.FileMode, syncDir func(dir string) error) error {
	return write(osFS{}, path, data, perm, true, syncDir)
}

// WriteWithoutSync is Write for derived data a crash may lose because the
// owner rebuilds or replays it (a cache, an index): the same exclusive temp
// file, umask-proof permissions and rename, with no fsync of the file or its
// directory.
func WriteWithoutSync(path string, data []byte, perm os.FileMode) error {
	return write(osFS{}, path, data, perm, false, nil)
}

// WriteFS is Write through an injected filesystem. A directory-sync failure
// wraps ErrPublished exactly as Write's does.
func WriteFS(fsys FS, path string, data []byte, perm os.FileMode) error {
	return write(fsys, path, data, perm, true, func(dir string) error {
		return SyncDirFS(fsys, dir)
	})
}

func write(fsys FS, path string, data []byte, perm os.FileMode, durable bool, syncDir func(dir string) error) error {
	dir := filepath.Dir(path)
	temp, err := createTemp(fsys, dir, filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tempPath := temp.Name()
	published := false
	defer func() {
		if !published {
			_ = fsys.Remove(tempPath)
		}
	}()
	n, err := temp.Write(data)
	if err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if n != len(data) {
		_ = temp.Close()
		return fmt.Errorf("write temp file for %s: %w", path, io.ErrShortWrite)
	}
	if err := fsys.Chmod(tempPath, perm); err != nil {
		_ = temp.Close()
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if durable {
		if err := temp.Sync(); err != nil && !SyncUnsupported(err) {
			_ = temp.Close()
			return fmt.Errorf("sync temp file for %s: %w", path, err)
		}
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := fsys.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename temp file over %s: %w", path, err)
	}
	published = true
	if !durable {
		return nil
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("%w: %w", ErrPublished, err)
	}
	return nil
}

// createTemp opens a new exclusively created temp file in dir. It mirrors
// os.CreateTemp's retry rule without requiring an *os.File.
func createTemp(fsys FS, dir, prefix string) (File, error) {
	for range 10000 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, prefix+hex.EncodeToString(suffix[:]))
		f, err := fsys.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		return f, err
	}
	return nil, errors.New("exhausted temp file names")
}

// Remove deletes the file at path and syncs its directory so the deletion is
// durable. A file that is already gone is not an error, but its directory is
// still synced: the earlier attempt may have removed it and failed to sync.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	if err := SyncDir(filepath.Dir(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SyncDir flushes dir's entries so a rename or removal inside it survives a
// crash. A filesystem that cannot sync directories at all is tolerated.
func SyncDir(dir string) error {
	return SyncDirFS(osFS{}, dir)
}

// SyncDirFS is SyncDir through an injected filesystem.
func SyncDirFS(fsys FS, dir string) error {
	if runtime.GOOS == "windows" {
		// Windows cannot flush a directory through a handle; NTFS journals
		// the rename itself.
		return nil
	}
	d, err := fsys.Open(dir)
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

// osFS is the production filesystem behind Write, WriteWithDirSync,
// WriteWithoutSync and SyncDir.
type osFS struct{}

func (osFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (osFS) Open(name string) (File, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (osFS) Rename(oldname, newname string) error { return os.Rename(oldname, newname) }

func (osFS) Chmod(name string, mode os.FileMode) error { return os.Chmod(name, mode) }

func (osFS) Remove(name string) error { return os.Remove(name) }
