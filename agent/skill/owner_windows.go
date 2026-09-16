//go:build windows

package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os/user"
)

// processOwnerTag names the current user for the private cache directory, using
// the account's SID so another account cannot share the name.
func processOwnerTag() string {
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return "user"
	}
	sum := sha256.Sum256([]byte(current.Uid))
	return hex.EncodeToString(sum[:8])
}

// cacheDirOwnedByCurrentUser cannot be checked from fs.FileInfo: Windows ACLs
// are not exposed there. The base lives under the per-user cache directory,
// which the OS protects, and is named for this account's SID; a reparse point
// (junction or symlink) under the name is rejected by the Lstat checks before
// this is consulted.
func cacheDirOwnedByCurrentUser(fs.FileInfo) bool { return true }

// cacheDirHasPrivatePermissions cannot be checked portably: Windows synthesizes
// directory modes, so a 0700 directory does not read back as 0700.
func cacheDirHasPrivatePermissions(fs.FileInfo) bool { return true }

// cacheDirOwnerCanWrite cannot be checked from a synthesized directory mode; the
// per-user temp directory is assumed writable by its owner.
func cacheDirOwnerCanWrite(fs.FileInfo) bool { return true }

// tempRootTrusted cannot be checked portably on Windows: ACLs are not exposed
// through fs.FileInfo. The cache root is the user cache directory, which the OS
// protects with per-user ACLs, and the temp root is per-user by convention.
func tempRootTrusted(fs.FileInfo) bool { return true }

// processCopyRootTrusted refuses the temp root for the process-lifetime
// extraction. Nothing here can verify from fs.FileInfo who created a directory
// or who may replace an entry in it, and the temp root is not the per-user cache
// directory the shared copy is protected by, so the degraded copy fails closed
// rather than extract skill content into a root the process cannot vouch for.
func processCopyRootTrusted(fs.FileInfo) bool { return false }

// ancestorDirTrusted cannot be checked portably on Windows either: the ACLs
// that decide whether another account can write an ancestor are not exposed
// through fs.FileInfo, and the user cache directory's chain is protected by the
// OS. The accepted limit is the same as tempRootTrusted's.
func ancestorDirTrusted(fs.FileInfo) bool { return true }
