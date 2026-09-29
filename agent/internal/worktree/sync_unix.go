//go:build unix

package worktree

import (
	"errors"
	"os"
	"syscall"
)

// syncDir flushes dir's directory entries so a rename that just committed to it
// survives power loss; the renamed file's own data is flushed separately before
// the rename. The caller is responsible for having renamed within dir. A
// filesystem that cannot sync a directory at all (some NFS/FUSE or overlay
// mounts) reports ENOSYS/ENOTSUP/EINVAL; the rename has already committed and
// the file's bytes are already flushed, so that is not an error this update
// should fail on, matching clientMutationSyncUnsupported's tolerance in
// agent/session_client_mutation_persist.go.
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	err = errors.Join(syncErr, closeErr)
	if syncUnsupported(err) {
		return nil
	}
	return err
}

// syncUnsupported reports whether err is the errno set a filesystem uses to say
// it cannot sync a directory at all.
func syncUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EINVAL)
}
