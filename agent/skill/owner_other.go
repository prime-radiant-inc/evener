//go:build !unix && !windows

package skill

import "io/fs"

// processOwnerTag is fixed on platforms whose temp directory is already
// per-user, so there is no second user to namespace against.
func processOwnerTag() string { return "user" }

// cacheDirOwnedByCurrentUser cannot check ownership portably; the temp
// directory is per-user on these platforms.
func cacheDirOwnedByCurrentUser(fs.FileInfo) bool { return true }

// cacheDirHasPrivatePermissions cannot be checked portably on these platforms.
func cacheDirHasPrivatePermissions(fs.FileInfo) bool { return true }
