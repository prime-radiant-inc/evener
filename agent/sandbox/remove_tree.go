package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// removeTree removes dir and everything under it. Go writes its module cache
// read-only (0555 directories, 0444 files), so a plain os.RemoveAll fails on a
// scratch that ran `go` with GOMODCACHE inside it. On failure this makes every
// directory in the tree owner-writable, as `go clean -modcache` does, and tries
// once more. WalkDir never follows a symlink, so nothing outside dir changes mode.
func removeTree(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	// A walk error only means some directory could not be made writable; the
	// retry below reports whatever is still left.
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr == nil && d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil && info.Mode().Perm()&0o200 == 0 {
				_ = os.Chmod(path, info.Mode().Perm()|0o700)
			}
		}
		return nil
	})
	if retryErr := os.RemoveAll(dir); retryErr != nil {
		return errors.Join(err, retryErr)
	}
	return nil
}
