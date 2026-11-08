// Package durablefile publishes a file's new contents atomically and durably
// through an afero filesystem: a reader sees the old file or the complete new
// one, and a successful Replace means the new contents survive a crash.
package durablefile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/afero"

	"primeradiant.com/evener/execsupport/fsatomic"
)

// Replace writes data to a uniquely named temp file in path's directory,
// fsyncs it, sets perm outright so the process umask has no say, renames it
// over path, and fsyncs the directory so the rename itself is durable. A
// failure of that last step wraps fsatomic.ErrPublished: the new contents are
// already visible at path and only their durability is unconfirmed.
//
// The parent directory must exist. A failed write removes its temp file; a
// crash between the create and the rename leaves a stray "<name>.tmp-*" file
// behind, which is the cost of never reusing a name another process could
// plant. Unlike fsatomic.Write, Replace works through an injected afero
// filesystem so a store's production OS filesystem and its in-memory test
// filesystem take the identical durable path.
func Replace(fs afero.Fs, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tempPath := temp.Name()
	published := false
	defer func() {
		if !published {
			_ = fs.Remove(tempPath)
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
	if err := fs.Chmod(tempPath, perm); err != nil {
		_ = temp.Close()
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil && !fsatomic.SyncUnsupported(err) {
		_ = temp.Close()
		return fmt.Errorf("sync temp file for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename temp file over %s: %w", path, err)
	}
	published = true
	if err := SyncDir(fs, dir); err != nil {
		return fmt.Errorf("%w: %w", fsatomic.ErrPublished, err)
	}
	return nil
}

// SyncDir flushes dir's entries so a rename or removal inside it survives a
// crash. A filesystem that cannot sync directories at all is tolerated, as is
// Windows, which journals the rename itself and cannot flush a directory
// through a handle.
func SyncDir(fs afero.Fs, dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := fs.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil && !fsatomic.SyncUnsupported(err) {
		_ = d.Close()
		return fmt.Errorf("sync directory %s: %w", dir, err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close directory %s: %w", dir, err)
	}
	return nil
}
