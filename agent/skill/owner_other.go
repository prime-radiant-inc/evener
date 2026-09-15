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

// cacheDirOwnerCanWrite cannot be checked portably on these platforms.
func cacheDirOwnerCanWrite(fs.FileInfo) bool { return true }

// tempRootTrusted cannot be checked portably on these platforms; their temp
// directory is per-user, which is the accepted limit of the check.
func tempRootTrusted(fs.FileInfo) bool { return true }

// ancestorDirTrusted cannot be checked portably on these platforms; their temp
// directory is per-user, which is the accepted limit of the check.
func ancestorDirTrusted(fs.FileInfo) bool { return true }
