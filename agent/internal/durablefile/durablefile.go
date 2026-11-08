// Package durablefile publishes a file's new contents atomically and durably
// through an afero filesystem: a reader sees the old file or the complete new
// one, and a successful Replace means the new contents survive a crash.
//
// It is the afero adapter over execsupport/fsatomic, which owns the write,
// sync and rename sequence so the OS and injected filesystems take one path.
package durablefile

import (
	"os"

	"github.com/spf13/afero"

	"primeradiant.com/evener/execsupport/fsatomic"
)

// Replace publishes data at path through fs with fsatomic.WriteFS semantics:
// exclusive temp file, length-checked write, fsync, umask-proof permission,
// rename, and a parent-directory fsync. A failure of that last step wraps
// fsatomic.ErrPublished: the new contents are already visible at path and only
// their durability is unconfirmed.
//
// The parent directory must exist. Replace makes the file durable in that
// directory; it does not sync the entries of directories the caller had to
// create to get there, the same boundary fsatomic.Write documents. A failed
// write removes its temp file; a crash between the create and the rename
// leaves one stray "<name>.tmp-*" file behind per crash, which nothing sweeps
// — the cost of never reusing a name another process could plant.
func Replace(fs afero.Fs, path string, data []byte, perm os.FileMode) error {
	return fsatomic.WriteFS(aferoFS{fs: fs}, path, data, perm)
}

// SyncDir flushes dir's entries so a rename or removal inside it survives a
// crash, with the filesystem's own Open and Sync.
func SyncDir(fs afero.Fs, dir string) error {
	return fsatomic.SyncDirFS(aferoFS{fs: fs}, dir)
}

// aferoFS adapts afero.Fs to fsatomic.FS. The per-method returns are typed
// because a method returning (afero.File, error) does not satisfy one
// returning (fsatomic.File, error).
type aferoFS struct {
	fs afero.Fs
}

func (a aferoFS) OpenFile(name string, flag int, perm os.FileMode) (fsatomic.File, error) {
	f, err := a.fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return aferoFile{File: f, fs: a.fs}, nil
}

func (a aferoFS) Open(name string) (fsatomic.File, error) {
	f, err := a.fs.Open(name)
	if err != nil {
		return nil, err
	}
	return aferoFile{File: f, fs: a.fs}, nil
}

func (a aferoFS) Rename(oldname, newname string) error { return a.fs.Rename(oldname, newname) }

func (a aferoFS) Remove(name string) error { return a.fs.Remove(name) }

// aferoFile keeps the descriptor-based chmod the OS path uses. afero.File has
// no Chmod, so an OS-backed handle is asserted back to *os.File; a filesystem
// with no descriptor fallback (the in-memory test filesystem) chmods by path.
type aferoFile struct {
	afero.File
	fs afero.Fs
}

func (f aferoFile) Chmod(mode os.FileMode) error {
	if osFile, ok := f.File.(*os.File); ok {
		return osFile.Chmod(mode)
	}
	return f.fs.Chmod(f.Name(), mode)
}
