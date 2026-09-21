//go:build !linux && !darwin

package agent

import "os"

// createAttachmentFile is the portable fallback: this platform has no
// O_NOFOLLOW, so a leaf symlink is followed there, but the create-exclusive
// half of the contract (never overwrite an existing entry) still holds.
func createAttachmentFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}
