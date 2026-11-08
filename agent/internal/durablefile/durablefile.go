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
// The parent directory must exist. A failed write removes its temp file; a
// crash between the create and the rename leaves a stray "<name>.tmp-*" file
// behind, which is the cost of never reusing a name another process could
// plant.
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
	return f, nil
}

func (a aferoFS) Open(name string) (fsatomic.File, error) {
	f, err := a.fs.Open(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (a aferoFS) Rename(oldname, newname string) error { return a.fs.Rename(oldname, newname) }

func (a aferoFS) Chmod(name string, mode os.FileMode) error { return a.fs.Chmod(name, mode) }

func (a aferoFS) Remove(name string) error { return a.fs.Remove(name) }
