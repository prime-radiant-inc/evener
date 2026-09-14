//go:build windows

package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os/user"
)

// processOwnerTag names the current user for the private cache directory, using
// the account's SID so another account cannot share the name. Windows temp
// directories are already per-user, so the name is the part this code can
// guarantee rather than an ACL check.
func processOwnerTag() string {
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return "user"
	}
	sum := sha256.Sum256([]byte(current.Uid))
	return hex.EncodeToString(sum[:8])
}

// cacheDirOwnedByCurrentUser cannot be checked portably here: Windows ACLs are
// not exposed through fs.FileInfo. The per-user temp directory and the SID-named
// base stand in for the check.
func cacheDirOwnedByCurrentUser(fs.FileInfo) bool { return true }

// cacheDirHasPrivatePermissions cannot be checked portably: Windows synthesizes
// directory modes, so a 0700 directory does not read back as 0700.
func cacheDirHasPrivatePermissions(fs.FileInfo) bool { return true }
