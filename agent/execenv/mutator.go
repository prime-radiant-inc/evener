//go:build darwin || linux

package execenv

import (
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// LocalExecutionEnvironment implements the FileMutator capability so apply_patch
// routes its file mutations through the same enforcement seam as the other file
// tools. When the environment carries an enforced sandbox policy, each method uses
// the fd-anchored, symlink-refusing layer (e.sandbox()); otherwise it confines to
// the working root via resolveWrite — the same containment the off-mode
// write_file/edit_file tools use, which replaces apply_patch's old lexical
// safeJoin check.

// ReadFileRaw returns the raw bytes of path, confined to the working root.
func (e *LocalExecutionEnvironment) ReadFileRaw(path string) ([]byte, error) {
	if sfs := e.sandbox(); sfs != nil {
		return sfs.readFile("apply_patch", e.resolve(path))
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return nil, err
	}
	return afero.ReadFile(e.filesystem(), abs)
}

// WriteFileRaw writes data to path, creating missing parents, confined to the
// working root (sandboxed: atomic temp+renameat beneath a writable root).
func (e *LocalExecutionEnvironment) WriteFileRaw(path string, data []byte, perm os.FileMode) error {
	if sfs := e.sandbox(); sfs != nil {
		return sfs.writeFile("apply_patch", e.resolve(path), data, perm)
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return err
	}
	if err := e.filesystem().MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return afero.WriteFile(e.filesystem(), abs, data, perm)
}

// RemovePath deletes path best-effort (a missing target is not an error), but an
// out-of-policy target is a denial.
func (e *LocalExecutionEnvironment) RemovePath(path string) error {
	if sfs := e.sandbox(); sfs != nil {
		return sfs.remove("apply_patch", e.resolve(path))
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return err
	}
	_ = e.filesystem().Remove(abs)
	return nil
}

// RenamePath moves oldPath to newPath, creating newPath's parents. Both endpoints
// are confined to the working root.
func (e *LocalExecutionEnvironment) RenamePath(oldPath, newPath string) error {
	if sfs := e.sandbox(); sfs != nil {
		return sfs.rename("apply_patch", e.resolve(oldPath), e.resolve(newPath))
	}
	oldAbs, err := e.resolveWrite(oldPath)
	if err != nil {
		return err
	}
	newAbs, err := e.resolveWrite(newPath)
	if err != nil {
		return err
	}
	if err := e.filesystem().MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
		return err
	}
	return e.filesystem().Rename(oldAbs, newAbs)
}
