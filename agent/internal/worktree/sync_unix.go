//go:build unix

package worktree

import (
	"errors"
	"os"
)

// syncDir flushes dir's directory entries so a rename that just committed to it
// survives power loss; the renamed file's own data is flushed separately before
// the rename. The caller is responsible for having renamed within dir.
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	return errors.Join(syncErr, closeErr)
}
