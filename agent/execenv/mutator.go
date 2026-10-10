package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/spf13/afero"
)

// LocalExecutionEnvironment implements the FileMutator capability so apply_patch
// routes its file mutations through the same enforcement seam as the other file
// tools. When the environment carries a file-tool-confined sandbox policy, each
// method uses the fd-anchored, symlink-refusing layer (e.sandbox()); otherwise it
// resolves relative paths against the working directory without restricting
// unrestricted off sessions to that root. Allocated scratch keeps its own
// fd-anchored contract.

// ReadFileRaw returns the raw bytes of path subject to the active policy.
func (e *LocalExecutionEnvironment) ReadFileRaw(path string) ([]byte, error) {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		return sfs.readFile("apply_patch", e.resolve(path))
	}
	abs := e.resolve(path)
	if sfs := e.scratchSandboxFor(abs); sfs != nil {
		defer sfs.release()
		return sfs.readFile("apply_patch", abs)
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return nil, err
	}
	return afero.ReadFile(e.filesystem(), abs)
}

// WriteFileRaw writes data to path, creating missing parents, confined to the
// active policy (sandboxed: atomic temp+renameat beneath a writable root).
func (e *LocalExecutionEnvironment) WriteFileRaw(path string, data []byte, perm os.FileMode) error {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		return sfs.writeFile("apply_patch", e.resolve(path), data, perm)
	}
	abs := e.resolve(path)
	if sfs := e.scratchSandboxFor(abs); sfs != nil {
		defer sfs.release()
		return sfs.writeFile("apply_patch", abs, data, perm)
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
// out-of-policy target is a denial. Any other remove failure — permission, a
// read-only filesystem, a nonempty directory — is surfaced, so a delete that did
// not happen is never reported as success (issue #2376).
func (e *LocalExecutionEnvironment) RemovePath(path string) error {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		return sfs.remove("apply_patch", e.resolve(path))
	}
	abs := e.resolve(path)
	if sfs := e.scratchSandboxFor(abs); sfs != nil {
		defer sfs.release()
		return sfs.remove("apply_patch", abs)
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return err
	}
	if err := e.filesystem().Remove(abs); err != nil && !isAbsentRemove(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// isAbsentRemove reports whether a remove error means the target is already
// absent: ENOENT (missing target or parent) or ENOTDIR (an ancestor is not a
// directory). Those are the only remove failures a best-effort delete may treat
// as a no-op success; every other failure must propagate (issue #2376).
func isAbsentRemove(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// RenamePath moves oldPath to newPath, creating newPath's parents. Both endpoints
// must satisfy the active policy.
func (e *LocalExecutionEnvironment) RenamePath(oldPath, newPath string) error {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		return sfs.rename("apply_patch", e.resolve(oldPath), e.resolve(newPath))
	}
	oldAbs := e.resolve(oldPath)
	newAbs := e.resolve(newPath)
	if sfs := e.scratchSandboxFor(oldAbs); sfs != nil {
		defer sfs.release()
		if targetFS := e.scratchSandboxFor(newAbs); targetFS != nil {
			defer targetFS.release()
			// Both endpoints are inside the same one-session scratch root. Reuse
			// the first layer's cached root fd; its policy root is identical.
			return sfs.rename("apply_patch", oldAbs, newAbs)
		}
		return fmt.Errorf("%s: cannot rename from the session scratch root outside that root", newPath)
	}
	if targetFS := e.scratchSandboxFor(newAbs); targetFS != nil {
		defer targetFS.release()
		return fmt.Errorf("%s: cannot rename into the session scratch root from outside that root", newPath)
	}
	var err error
	oldAbs, err = e.resolveWrite(oldPath)
	if err != nil {
		return err
	}
	newAbs, err = e.resolveWrite(newPath)
	if err != nil {
		return err
	}
	if err := e.filesystem().MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
		return err
	}
	return e.filesystem().Rename(oldAbs, newAbs)
}
